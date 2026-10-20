package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"clinic/internal/auth"
)

type fakeLogin struct {
	res        auth.LoginResult
	err        error
	calls      int
	user, pass string
}

func (f *fakeLogin) Login(_ context.Context, username, plain string) (auth.LoginResult, error) {
	f.calls++
	f.user, f.pass = username, plain
	return f.res, f.err
}

func loginHandler(authn Authenticator, svc LoginService) http.Handler {
	return NewServer(authn, discardLogger(), NewAuthModule(svc)).Handler()
}

const validLogin = `{"username":"alice_1","password":"correct-horse"}`

func TestLoginSuccess(t *testing.T) {
	expires := time.Date(2026, 10, 19, 9, 15, 0, 0, time.UTC)
	svc := &fakeLogin{res: auth.LoginResult{Token: "tok", ExpiresAt: expires}}
	authn := authnFor()

	rec := do(loginHandler(authn, svc), "POST", "/login", "", validLogin)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	if authn.calls != 0 {
		t.Errorf("authenticator called %d times on an open route", authn.calls)
	}
	if svc.calls != 1 || svc.user != "alice_1" || svc.pass != "correct-horse" {
		t.Errorf("service got (%q, %q) in %d calls", svc.user, svc.pass, svc.calls)
	}

	var got tokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Token != "tok" || got.TokenType != "Bearer" || !got.ExpiresAt.Equal(expires) {
		t.Errorf("response = %+v", got)
	}
}

func TestLoginRejectsBadBodies(t *testing.T) {
	cases := map[string]string{
		"empty":         "",
		"malformed":     `{"username":`,
		"wrong type":    `{"username":5}`,
		"unknown field": `{"username":"a","password":"b","role":"admin"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			svc := &fakeLogin{}
			rec := do(loginHandler(authnFor(), svc), "POST", "/login", "", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body)
			}
			if svc.calls != 0 {
				t.Error("service was called for a rejected request")
			}
		})
	}
}

func TestLoginErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"invalid credentials", auth.ErrInvalidCredentials, 401, "invalid_credentials"},
		{"wrapped", fmt.Errorf("login: %w", auth.ErrInvalidCredentials), 401, "invalid_credentials"},
		{"internal", errors.New("connection refused: password=hunter2"), 500, "internal_error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(loginHandler(authnFor(), &fakeLogin{err: c.err}), "POST", "/login", "", validLogin)
			e := decodeErr(t, rec)
			if rec.Code != c.status || e.Error.Code != c.code {
				t.Fatalf("response = %d %+v, want %d %s", rec.Code, e, c.status, c.code)
			}
			if body := rec.Body.String(); strings.Contains(body, "hunter2") || strings.Contains(body, `"token"`) {
				t.Errorf("response leaks detail or a token: %s", body)
			}
		})
	}
}

// Any new open route has to be added here on purpose.
func TestOpenRoutesAreExactlyTheIntendedSet(t *testing.T) {
	open := NewServer(authnFor(), discardLogger(),
		NewHealthModule(fakePinger{}),
		NewAccountsModule(&fakeRegistrar{}, &fakeIssuer{}),
		NewAuthModule(&fakeLogin{}),
	).OpenRoutes()
	slices.Sort(open)

	want := []string{"GET /healthz", "POST /login", "POST /patients"}
	if !slices.Equal(open, want) {
		t.Fatalf("open routes = %v, want %v", open, want)
	}
}
