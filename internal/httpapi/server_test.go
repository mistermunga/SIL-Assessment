package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"clinic/internal/apperr"
	"clinic/internal/auth"
)

var errTestConflict = errors.New("test conflict")

var (
	patientP = auth.Principal{UserID: 1, Username: "pat", Role: auth.RolePatient}
	doctorP  = auth.Principal{UserID: 2, Username: "doc", Role: auth.RoleDoctor}
	adminP   = auth.Principal{UserID: 3, Username: "adm", Role: auth.RoleAdmin}
)

type fakeAuthn struct {
	principals map[string]auth.Principal
	err        error // returned for every call when set
	calls      int
}

func (f *fakeAuthn) Authenticate(_ context.Context, token string) (auth.Principal, error) {
	f.calls++
	if f.err != nil {
		return auth.Principal{}, f.err
	}
	p, ok := f.principals[token]
	if !ok {
		return auth.Principal{}, auth.ErrInvalidToken
	}
	return p, nil
}

func authnFor() *fakeAuthn {
	return &fakeAuthn{principals: map[string]auth.Principal{
		"pat-token": patientP, "doc-token": doctorP, "adm-token": adminP,
	}}
}

func fail(err error) HandlerFunc {
	return func(http.ResponseWriter, *http.Request) error { return err }
}

type testModule struct{}

func (testModule) ErrorRules() []ErrorRule {
	return []ErrorRule{{Is: errTestConflict, Status: http.StatusConflict, Code: "test_conflict"}}
}

func (testModule) Routes() []Route {
	hello := func(w http.ResponseWriter, r *http.Request) error {
		return writeJSON(w, http.StatusOK, map[string]any{"user_id": principalFrom(r.Context()).UserID})
	}
	return []Route{
		{Pattern: "GET /open", Handler: hello},
		{Pattern: "GET /any", Access: Authenticated, Handler: hello},
		{Pattern: "GET /patient", Access: Patient, Handler: hello},
		{Pattern: "GET /doctor", Access: Doctor, Handler: hello},
		{Pattern: "GET /admin", Access: Admin, Handler: hello},
		{Pattern: "GET /either", Access: AnyOf(Patient, Admin), Handler: hello},
		{Pattern: "GET /domain", Handler: fail(fmt.Errorf("%w: slot 10:00", errTestConflict))},
		{Pattern: "GET /invalid", Handler: fail(&apperr.ValidationError{
			Problems: map[string]string{"start": "is required"}})},
		{Pattern: "GET /boom", Handler: fail(errors.New("connection refused: password=hunter2"))},
		{Pattern: "GET /panic", Handler: func(http.ResponseWriter, *http.Request) error { panic("kaboom") }},
		{Pattern: "POST /echo", Handler: func(w http.ResponseWriter, r *http.Request) error {
			var in struct {
				Name string `json:"name"`
			}
			if err := readJSON(w, r, &in); err != nil {
				return err
			}
			return writeJSON(w, http.StatusOK, in)
		}},
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testHandler(authn Authenticator) http.Handler {
	return NewServer(authn, discardLogger(), testModule{}).Handler()
}

func do(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type errResp struct {
	Error struct {
		Code     string            `json:"code"`
		Message  string            `json:"message"`
		Problems map[string]string `json:"problems"`
	} `json:"error"`
}

func decodeErr(t *testing.T, rec *httptest.ResponseRecorder) errResp {
	t.Helper()
	var e errResp
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("body is not the error format: %q (%v)", rec.Body.String(), err)
	}
	return e
}

func TestAccessRules(t *testing.T) {
	h := testHandler(authnFor())
	cases := []struct {
		name, path, token string
		want              int
	}{
		{"open without a token", "/open", "", 200},
		{"closed without a token", "/any", "", 401},
		{"closed with a bad token", "/any", "nope", 401},
		{"any role", "/any", "pat-token", 200},
		{"patient route, patient", "/patient", "pat-token", 200},
		{"patient route, doctor", "/patient", "doc-token", 403},
		{"doctor route, patient", "/doctor", "pat-token", 403},
		{"doctor route, doctor", "/doctor", "doc-token", 200},
		{"doctor route, admin", "/doctor", "adm-token", 200}, // an admin is a doctor
		{"admin route, doctor", "/admin", "doc-token", 403},
		{"admin route, admin", "/admin", "adm-token", 200},
		{"any-of, patient", "/either", "pat-token", 200},
		{"any-of, admin", "/either", "adm-token", 200},
		{"any-of, plain doctor", "/either", "doc-token", 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if rec := do(h, "GET", c.path, c.token, ""); rec.Code != c.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, c.want, rec.Body)
			}
		})
	}
}

func TestOpenRouteNeverTouchesTheAuthenticator(t *testing.T) {
	authn := authnFor()
	rec := do(testHandler(authn), "GET", "/open", "garbage", "")
	if rec.Code != 200 || authn.calls != 0 {
		t.Fatalf("status = %d, authenticator calls = %d, want 200 and 0", rec.Code, authn.calls)
	}
}

func TestPrincipalReachesTheHandler(t *testing.T) {
	rec := do(testHandler(authnFor()), "GET", "/any", "doc-token", "")
	var got struct {
		UserID int32 `json:"user_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.UserID != doctorP.UserID {
		t.Fatalf("user_id = (%d, %v), want %d", got.UserID, err, doctorP.UserID)
	}
}

func TestAuthorizationHeaderParsing(t *testing.T) {
	h := testHandler(authnFor())
	cases := map[string]int{
		"Bearer pat-token": 200,
		"bearer pat-token": 200, // the scheme is case-insensitive
		"Bearer":           401,
		"Bearer ":          401,
		"Basic pat-token":  401,
		"pat-token":        401,
	}
	for header, want := range cases {
		req := httptest.NewRequest("GET", "/any", nil)
		req.Header.Set("Authorization", header)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Authorization %q: status = %d, want %d", header, rec.Code, want)
		}
	}
}

func TestUnauthorizedAndForbiddenResponses(t *testing.T) {
	h := testHandler(authnFor())

	rec := do(h, "GET", "/any", "", "")
	if e := decodeErr(t, rec); rec.Code != 401 || e.Error.Code != "unauthorized" ||
		rec.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("401 response = %d %+v header %q", rec.Code, e, rec.Header().Get("WWW-Authenticate"))
	}

	rec = do(h, "GET", "/admin", "pat-token", "")
	if e := decodeErr(t, rec); rec.Code != 403 || e.Error.Code != "forbidden" {
		t.Errorf("403 response = %d %+v", rec.Code, e)
	}
}

func TestAuthenticatorFailureIs500Not401(t *testing.T) {
	rec := do(testHandler(&fakeAuthn{err: errors.New("db down")}), "GET", "/any", "pat-token", "")
	if rec.Code != 500 {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestErrorMapping(t *testing.T) {
	h := testHandler(authnFor())

	rec := do(h, "GET", "/domain", "", "")
	if e := decodeErr(t, rec); rec.Code != 409 || e.Error.Code != "test_conflict" ||
		!strings.Contains(e.Error.Message, "slot 10:00") {
		t.Errorf("domain error = %d %+v", rec.Code, e)
	}

	rec = do(h, "GET", "/invalid", "", "")
	if e := decodeErr(t, rec); rec.Code != 422 || e.Error.Code != "validation_failed" ||
		e.Error.Problems["start"] != "is required" {
		t.Errorf("validation error = %d %+v", rec.Code, e)
	}

	for path, secret := range map[string]string{"/boom": "hunter2", "/panic": "kaboom"} {
		rec = do(h, "GET", path, "", "")
		e := decodeErr(t, rec)
		if rec.Code != 500 || e.Error.Code != "internal_error" || strings.Contains(rec.Body.String(), secret) {
			t.Errorf("%s = %d %s, want a generic 500 that hides %q", path, rec.Code, rec.Body, secret)
		}
	}
}

func TestReadJSON(t *testing.T) {
	h := testHandler(authnFor())
	cases := []struct {
		name, body string
		want       int
	}{
		{"valid", `{"name":"x"}`, 200},
		{"empty body", ``, 400},
		{"malformed", `{"name":`, 400},
		{"unknown field", `{"nope":1}`, 400},
		{"wrong type", `{"name":5}`, 400},
		{"trailing data", `{"name":"x"} {"name":"y"}`, 400},
		{"too large", `{"name":"` + strings.Repeat("x", maxBodyBytes) + `"}`, 413},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if rec := do(h, "POST", "/echo", "", c.body); rec.Code != c.want {
				t.Fatalf("status = %d, want %d (body %.200s)", rec.Code, c.want, rec.Body)
			}
		})
	}
}

func TestOpenRoutesListsOnlyUnguardedRoutes(t *testing.T) {
	open := NewServer(authnFor(), discardLogger(), testModule{}).OpenRoutes()
	if !slices.Contains(open, "GET /open") {
		t.Error("GET /open missing from the open routes")
	}
	for _, guarded := range []string{"GET /any", "GET /patient", "GET /doctor", "GET /admin", "GET /either"} {
		if slices.Contains(open, guarded) {
			t.Errorf("%s listed as open", guarded)
		}
	}
}

type fakePinger struct{ err error }

func (p fakePinger) Ping(context.Context) error { return p.err }

func TestHealth(t *testing.T) {
	up := NewServer(authnFor(), discardLogger(), NewHealthModule(fakePinger{})).Handler()
	if rec := do(up, "GET", "/healthz", "", ""); rec.Code != 200 {
		t.Errorf("healthy: status = %d, want 200 with no token", rec.Code)
	}

	down := NewServer(authnFor(), discardLogger(),
		NewHealthModule(fakePinger{err: errors.New("refused")})).Handler()
	rec := do(down, "GET", "/healthz", "", "")
	if e := decodeErr(t, rec); rec.Code != 503 || e.Error.Code != "unavailable" ||
		strings.Contains(rec.Body.String(), "refused") {
		t.Errorf("unhealthy = %d %s, want 503 without the cause", rec.Code, rec.Body)
	}
}
