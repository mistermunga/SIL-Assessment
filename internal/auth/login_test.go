package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"clinic/internal/auth"
)

func TestLogin(t *testing.T) {
	svc, _ := setup(t)
	ctx := context.Background()

	u, err := svc.RegisterPatient(ctx, input("alice", "alice@example.com"))
	if err != nil {
		t.Fatal(err)
	}

	// username is normalised, so different case and spacing still match
	res, err := svc.Login(ctx, " ALICE ", testPassword)
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	id, err := auth.NewTokenManager(testSecret, time.Minute).Verify(res.Token)
	if err != nil || id != u.UserID {
		t.Errorf("token user = (%d, %v), want %d", id, err, u.UserID)
	}
	if !res.ExpiresAt.After(time.Now()) {
		t.Error("ExpiresAt is not in the future")
	}
}

func TestLoginFailures(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()

	if _, err := svc.RegisterPatient(ctx, input("alice", "alice@example.com")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RegisterPatient(ctx, input("gone", "gone@example.com")); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE users SET deleted_at = now() WHERE username = 'gone'"); err != nil {
		t.Fatal(err)
	}

	cases := []struct{ name, username, password string }{
		{"wrong password", "alice", "not-the-password"},
		{"unknown user", "nobody", testPassword},
		{"deleted user", "gone", testPassword},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := svc.Login(ctx, c.username, c.password)
			if !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Fatalf("err = %v, want ErrInvalidCredentials", err)
			}
		})
	}
}
