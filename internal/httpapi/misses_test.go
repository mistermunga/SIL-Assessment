package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type pathModule struct{}

func (pathModule) Routes() []Route {
	return []Route{{
		Pattern: "GET /things/{id}",
		Handler: func(w http.ResponseWriter, r *http.Request) error {
			return writeJSON(w, http.StatusOK, map[string]string{"id": r.PathValue("id")})
		},
	}}
}

func TestUnknownRouteReturnsJSON404(t *testing.T) {
	h := testHandler(authnFor())

	for _, token := range []string{"", "garbage", "pat-token"} {
		rec := do(h, "GET", "/no/such/thing", token, "")
		e := decodeErr(t, rec)
		if rec.Code != 404 || e.Error.Code != "route_not_found" {
			t.Errorf("token %q: response = %d %+v, want 404 route_not_found", token, rec.Code, e)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		if strings.Contains(rec.Body.String(), "page not found") {
			t.Errorf("ServeMux's plain-text body leaked: %s", rec.Body)
		}
	}
}

func TestWrongMethodReturnsJSON405WithAllow(t *testing.T) {
	h := testHandler(authnFor())
	cases := []struct {
		method, path string
		allows       string
		notAllows    string
	}{
		{"GET", "/echo", "POST", "GET"},
		{"POST", "/open", "GET", "POST"},
		{"DELETE", "/any", "GET", "DELETE"}, // a guarded route: 405 comes before the token check
	}
	for _, c := range cases {
		rec := do(h, c.method, c.path, "", "")
		e := decodeErr(t, rec)
		if rec.Code != 405 || e.Error.Code != "method_not_allowed" {
			t.Errorf("%s %s: response = %d %+v, want 405 method_not_allowed", c.method, c.path, rec.Code, e)
		}
		allow := rec.Header().Get("Allow")
		if !strings.Contains(allow, c.allows) || strings.Contains(allow, c.notAllows) {
			t.Errorf("%s %s: Allow = %q, want it to list %s and not %s", c.method, c.path, allow, c.allows, c.notAllows)
		}
	}
}

func TestMatchedRoutesAreUnaffected(t *testing.T) {
	h := NewServer(authnFor(), discardLogger(), pathModule{}, testModule{}).Handler()

	// path values still reach the handler
	rec := do(h, "GET", "/things/42", "", "")
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != 200 || got["id"] != "42" {
		t.Errorf("response = %d %s (%v), want id 42", rec.Code, rec.Body, err)
	}

	// HEAD is served by GET routes
	if rec := do(h, "HEAD", "/open", "", ""); rec.Code != 200 {
		t.Errorf("HEAD /open: status = %d, want 200", rec.Code)
	}
}
