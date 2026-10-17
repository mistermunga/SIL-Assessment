package auth_test

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"clinic/internal/auth"
)

var testSecret = []byte("test-secret-at-least-32-bytes-long!!")

func TestTokenRoundTrip(t *testing.T) {
	m := auth.NewTokenManager(testSecret, time.Minute)

	raw, _, err := m.Issue(42)
	if err != nil {
		t.Fatal(err)
	}
	id, err := m.Verify(raw)
	if err != nil || id != 42 {
		t.Fatalf("Verify = (%d, %v), want (42, nil)", id, err)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	m := auth.NewTokenManager(testSecret, -time.Minute) // already expired when issued
	raw, _, _ := m.Issue(42)

	if _, err := m.Verify(raw); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken", err)
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	raw, _, _ := auth.NewTokenManager(testSecret, time.Minute).Issue(42)
	other := auth.NewTokenManager([]byte("a-different-secret-of-32-bytes!!!!!"), time.Minute)

	if _, err := other.Verify(raw); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken", err)
	}
}

func TestVerifyRejectsOtherAlgorithm(t *testing.T) {
	claims := jwt.RegisteredClaims{
		Issuer:    "clinic",
		Subject:   "42",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
	}
	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS512, claims).SignedString(testSecret)
	if err != nil {
		t.Fatal(err)
	}

	m := auth.NewTokenManager(testSecret, time.Minute)
	if _, err := m.Verify(raw); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("HS512 token accepted: err = %v", err)
	}
}

func TestVerifyRejectsGarbage(t *testing.T) {
	m := auth.NewTokenManager(testSecret, time.Minute)
	for _, raw := range []string{"", "abc", "a.b.c"} {
		if _, err := m.Verify(raw); !errors.Is(err, auth.ErrInvalidToken) {
			t.Errorf("Verify(%q) err = %v, want ErrInvalidToken", raw, err)
		}
	}
}
