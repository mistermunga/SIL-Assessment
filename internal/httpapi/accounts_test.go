package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"clinic/internal/accounts"
	"clinic/internal/apperr"
	"clinic/internal/store"
)

type fakeRegistrar struct {
	user  store.User
	err   error
	calls int
	got   accounts.RegisterInput
}

func (f *fakeRegistrar) RegisterPatient(_ context.Context, in accounts.RegisterInput) (store.User, error) {
	f.calls++
	f.got = in
	return f.user, f.err
}

type fakeIssuer struct {
	err error
	ids []int32
}

func (f *fakeIssuer) Issue(id int32) (string, time.Time, error) {
	f.ids = append(f.ids, id)
	if f.err != nil {
		return "", time.Time{}, f.err
	}
	return "test-token", time.Date(2026, 10, 19, 9, 15, 0, 0, time.UTC), nil
}

func accountsHandler(authn Authenticator, reg Registrar, issuer TokenIssuer) http.Handler {
	return NewServer(authn, discardLogger(), NewAccountsModule(reg, issuer)).Handler()
}

const validRegistration = `{"username":"alice_1","email":"a@example.com","password":"correct-horse","firstname":"Alice","lastname":"Lee"}`

func TestRegisterPatientSuccess(t *testing.T) {
	created := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	reg := &fakeRegistrar{user: store.User{
		UserID: 7, Username: "alice_1", Email: "a@example.com", PasswordHash: "SECRET-HASH",
		Firstname: pgtype.Text{String: "Alice", Valid: true},
		Lastname:  pgtype.Text{String: "Lee", Valid: true},
		CreatedAt: created,
	}}
	issuer := &fakeIssuer{}
	authn := authnFor()

	rec := do(accountsHandler(authn, reg, issuer), "POST", "/patients", "", validRegistration)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body)
	}
	if authn.calls != 0 {
		t.Errorf("authenticator called %d times on an open route", authn.calls)
	}

	want := accounts.RegisterInput{
		Username: "alice_1", Email: "a@example.com", Password: "correct-horse",
		Firstname: "Alice", Lastname: "Lee",
	}
	if reg.calls != 1 || reg.got != want {
		t.Errorf("service got %+v (%d calls), want %+v", reg.got, reg.calls, want)
	}
	if len(issuer.ids) != 1 || issuer.ids[0] != 7 {
		t.Errorf("token issued for %v, want [7]", issuer.ids)
	}

	var got struct {
		User      userResponse `json:"user"`
		Token     string       `json:"token"`
		TokenType string       `json:"token_type"`
		ExpiresAt time.Time    `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.User.ID != 7 || got.User.Username != "alice_1" || got.User.Email != "a@example.com" ||
		got.User.Firstname != "Alice" || got.User.Lastname != "Lee" || got.User.Role != "patient" ||
		!got.User.CreatedAt.Equal(created) {
		t.Errorf("user = %+v", got.User)
	}
	if got.Token != "test-token" || got.TokenType != "Bearer" || got.ExpiresAt.IsZero() {
		t.Errorf("token fields = %q %q %v", got.Token, got.TokenType, got.ExpiresAt)
	}
	if body := rec.Body.String(); strings.Contains(body, "SECRET-HASH") || strings.Contains(strings.ToLower(body), "password") {
		t.Errorf("response leaks credentials: %s", body)
	}
}

func TestRegisterPatientRejectsBadBodies(t *testing.T) {
	cases := map[string]string{
		"empty":               "",
		"malformed":           `{"username":`,
		"wrong type":          `{"username":5}`,
		"role smuggled in":    `{"username":"alice_1","email":"a@example.com","password":"correct-horse","role":"admin"}`,
		"admin flag smuggled": `{"username":"alice_1","email":"a@example.com","password":"correct-horse","is_admin":true}`,
		"password hash field": `{"username":"alice_1","password_hash":"x"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			reg := &fakeRegistrar{}
			issuer := &fakeIssuer{}
			rec := do(accountsHandler(authnFor(), reg, issuer), "POST", "/patients", "", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body)
			}
			if reg.calls != 0 {
				t.Error("service was called for a rejected request")
			}
		})
	}
}

func TestRegisterPatientErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		status   int
		code     string
		problems map[string]string
	}{
		{"username taken", accounts.ErrUsernameTaken, 409, "username_taken", nil},
		{"email taken", accounts.ErrEmailTaken, 409, "email_taken", nil},
		{"wrapped duplicate", fmt.Errorf("register: %w", accounts.ErrUsernameTaken), 409, "username_taken", nil},
		{"validation", &apperr.ValidationError{Problems: map[string]string{"password": "must be at least 8 characters"}},
			422, "validation_failed", map[string]string{"password": "must be at least 8 characters"}},
		{"internal", errors.New("connection refused: password=hunter2"), 500, "internal_error", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			issuer := &fakeIssuer{}
			rec := do(accountsHandler(authnFor(), &fakeRegistrar{err: c.err}, issuer), "POST", "/patients", "", validRegistration)
			e := decodeErr(t, rec)
			if rec.Code != c.status || e.Error.Code != c.code {
				t.Fatalf("response = %d %+v, want %d %s", rec.Code, e, c.status, c.code)
			}
			for field, msg := range c.problems {
				if e.Error.Problems[field] != msg {
					t.Errorf("problems[%q] = %q, want %q", field, e.Error.Problems[field], msg)
				}
			}
			if strings.Contains(rec.Body.String(), "hunter2") {
				t.Error("internal error detail leaked to the client")
			}
		})
	}
}

func TestRegisterPatientRouteIsOpen(t *testing.T) {
	open := NewServer(authnFor(), discardLogger(), NewAccountsModule(&fakeRegistrar{}, &fakeIssuer{})).OpenRoutes()
	if len(open) != 1 || open[0] != "POST /patients" {
		t.Fatalf("open routes = %v, want only POST /patients", open)
	}
}
