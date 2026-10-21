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

type fakeDoctors struct {
	list  []accounts.DoctorSummary
	user  store.User
	err   error
	calls int
	got   accounts.DoctorInput
}

func (f *fakeDoctors) ListDoctors(context.Context) ([]accounts.DoctorSummary, error) {
	return f.list, nil
}

func (f *fakeDoctors) RegisterDoctor(_ context.Context, in accounts.DoctorInput) (store.User, error) {
	f.calls++
	f.got = in
	return f.user, f.err
}

func doctorsHandler(svc DoctorService) http.Handler {
	return NewServer(authnFor(), discardLogger(), NewDoctorsModule(svc)).Handler()
}

const validDoctor = `{"username":"dr_bob","email":"bob@example.com","password":"correct-horse","firstname":"Bob","lastname":"Ray"}`
const validAdminDoctor = `{"username":"dr_bob","email":"bob@example.com","password":"correct-horse","firstname":"Bob","lastname":"Ray","is_admin":true}`

func newDoctorUser() store.User {
	return store.User{
		UserID: 11, Username: "dr_bob", Email: "bob@example.com", PasswordHash: "SECRET-HASH",
		Firstname: pgtype.Text{String: "Bob", Valid: true},
		Lastname:  pgtype.Text{String: "Ray", Valid: true},
		CreatedAt: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC),
	}
}

func TestDoctorsList(t *testing.T) {
	svc := &fakeDoctors{list: []accounts.DoctorSummary{{ID: 3, Name: "Ann Alpha"}, {ID: 4, Name: "Carl Charlie"}}}
	h := doctorsHandler(svc)

	if rec := do(h, "GET", "/doctors", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", rec.Code)
	}

	rec := do(h, "GET", "/doctors", "pat-token", "")
	var got struct {
		Doctors []struct {
			ID   int32  `json:"id"`
			Name string `json:"name"`
		} `json:"doctors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != 200 ||
		len(got.Doctors) != 2 || got.Doctors[0].ID != 3 || got.Doctors[0].Name != "Ann Alpha" {
		t.Fatalf("response = %d %s (%v)", rec.Code, rec.Body, err)
	}

	if rec := do(doctorsHandler(&fakeDoctors{}), "GET", "/doctors", "pat-token", ""); !strings.Contains(rec.Body.String(), `"doctors":[]`) {
		t.Errorf("empty list = %s, want \"doctors\":[]", rec.Body)
	}
}

func TestCreateDoctorAccess(t *testing.T) {
	cases := []struct {
		name, token string
		want        int
	}{
		{"no token", "", 401},
		{"patient", "pat-token", 403},
		{"doctor", "doc-token", 403},
		{"admin", "adm-token", 201},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc := &fakeDoctors{user: newDoctorUser()}
			rec := do(doctorsHandler(svc), "POST", "/doctors", c.token, validDoctor)
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, c.want, rec.Body)
			}
			if wantCalls := map[bool]int{true: 1, false: 0}[c.want == 201]; svc.calls != wantCalls {
				t.Errorf("service calls = %d, want %d", svc.calls, wantCalls)
			}
		})
	}
}

func TestCreateDoctor(t *testing.T) {
	cases := []struct {
		name, body, role string
		isAdmin          bool
	}{
		{"plain doctor", validDoctor, "doctor", false},
		{"administrator", validAdminDoctor, "admin", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc := &fakeDoctors{user: newDoctorUser()}
			rec := do(doctorsHandler(svc), "POST", "/doctors", "adm-token", c.body)
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body)
			}

			want := accounts.DoctorInput{
				UserInfo: accounts.RegisterInput{
					Username: "dr_bob", Email: "bob@example.com", Password: "correct-horse",
					Firstname: "Bob", Lastname: "Ray",
				},
				IsAdmin: c.isAdmin,
			}
			if svc.got != want {
				t.Errorf("service got %+v, want %+v", svc.got, want)
			}

			var got userResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.ID != 11 || got.Username != "dr_bob" || got.Role != c.role {
				t.Errorf("response = %+v, want id 11 with role %s", got, c.role)
			}
			body := rec.Body.String()
			if strings.Contains(body, "SECRET-HASH") || strings.Contains(strings.ToLower(body), "password") ||
				strings.Contains(body, `"token"`) {
				t.Errorf("response leaks credentials or a token: %s", body)
			}
		})
	}
}

func TestCreateDoctorRejectsBadBodies(t *testing.T) {
	cases := map[string]string{
		"empty":               "",
		"malformed":           `{"username":`,
		"role smuggled in":    `{"username":"dr_bob","role":"admin"}`,
		"admin flag a string": `{"username":"dr_bob","is_admin":"yes"}`,
		"password hash":       `{"username":"dr_bob","password_hash":"x"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			svc := &fakeDoctors{user: newDoctorUser()}
			rec := do(doctorsHandler(svc), "POST", "/doctors", "adm-token", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body)
			}
			if svc.calls != 0 {
				t.Error("service was called for a rejected request")
			}
		})
	}
}

func TestCreateDoctorErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"username taken", accounts.ErrUsernameTaken, 409, "username_taken"},
		{"email taken", fmt.Errorf("register: %w", accounts.ErrEmailTaken), 409, "email_taken"},
		{"validation", &apperr.ValidationError{Problems: map[string]string{"password": "must be at least 8 characters"}},
			422, "validation_failed"},
		{"internal", errors.New("connection refused: password=hunter2"), 500, "internal_error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(doctorsHandler(&fakeDoctors{err: c.err}), "POST", "/doctors", "adm-token", validDoctor)
			e := decodeErr(t, rec)
			if rec.Code != c.status || e.Error.Code != c.code {
				t.Fatalf("response = %d %+v, want %d %s", rec.Code, e, c.status, c.code)
			}
			if strings.Contains(rec.Body.String(), "hunter2") {
				t.Error("internal detail leaked to the client")
			}
		})
	}
}
