package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"clinic/internal/auth"
)

func tokenFor(t *testing.T, svc *auth.Service, username string) string {
	t.Helper()
	res, err := svc.Login(context.Background(), username, testPassword)
	if err != nil {
		t.Fatalf("login %s: %v", username, err)
	}
	return res.Token
}

func TestAuthenticateRoles(t *testing.T) {
	svc, _ := setup(t)
	ctx := context.Background()

	pat, _ := svc.RegisterPatient(ctx, input("pat", "pat@example.com"))
	doc, _ := svc.RegisterDoctor(ctx, auth.DoctorInput{UserInfo: input("doc", "doc@example.com")})
	adm, _ := svc.RegisterDoctor(ctx, auth.DoctorInput{UserInfo: input("adm", "adm@example.com"), IsAdmin: true})

	cases := []struct {
		username string
		wantID   int32
		wantRole auth.Role
	}{
		{"pat", pat.UserID, auth.RolePatient},
		{"doc", doc.UserID, auth.RoleDoctor},
		{"adm", adm.UserID, auth.RoleAdmin},
	}
	for _, c := range cases {
		t.Run(c.username, func(t *testing.T) {
			p, err := svc.Authenticate(ctx, tokenFor(t, svc, c.username))
			if err != nil {
				t.Fatal(err)
			}
			if p.UserID != c.wantID || p.Role != c.wantRole || p.Username != c.username {
				t.Errorf("principal = %+v, want id %d role %s", p, c.wantID, c.wantRole)
			}
		})
	}
}

func TestAuthenticateReflectsCurrentState(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()

	if _, err := svc.RegisterDoctor(ctx, auth.DoctorInput{
		UserInfo: input("boss", "boss@example.com"), IsAdmin: true,
	}); err != nil {
		t.Fatal(err)
	}
	token := tokenFor(t, svc, "boss") // one token, used throughout

	p, err := svc.Authenticate(ctx, token)
	if err != nil || !p.IsAdmin() {
		t.Fatalf("before demotion: (%+v, %v), want admin", p, err)
	}

	if _, err := pool.Exec(ctx, "UPDATE doctors SET is_admin = false"); err != nil {
		t.Fatal(err)
	}
	p, err = svc.Authenticate(ctx, token)
	if err != nil || p.IsAdmin() || !p.IsDoctor() {
		t.Fatalf("after demotion: (%+v, %v), want plain doctor", p, err)
	}

	if _, err := pool.Exec(ctx, "UPDATE users SET deleted_at = now()"); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Authenticate(ctx, token); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("after deletion: err = %v, want ErrInvalidToken", err)
	}
}

func TestAuthenticateRejects(t *testing.T) {
	svc, _ := setup(t)
	ctx := context.Background()

	// valid signature, but the user doesn't exist
	raw, _, _ := auth.NewTokenManager(testSecret, 15*time.Minute).Issue(9999)

	for name, token := range map[string]string{"garbage": "abc", "unknown user": raw} {
		if _, err := svc.Authenticate(ctx, token); !errors.Is(err, auth.ErrInvalidToken) {
			t.Errorf("%s: err = %v, want ErrInvalidToken", name, err)
		}
	}
}
