package accounts_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"clinic/internal/accounts"
	"clinic/internal/apperr"
	"clinic/internal/password"
	"clinic/internal/store"
	"clinic/internal/testutil"
)

const testPassword = "correct-horse-battery"

func setup(t *testing.T) (*accounts.Service, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.NewPool(t)
	return accounts.NewService(store.NewStore(pool)), pool
}

func input(username, email string) accounts.RegisterInput {
	return accounts.RegisterInput{
		Username:  username,
		Email:     email,
		Password:  testPassword,
		Firstname: "Test",
		Lastname:  "User",
	}
}

func rowCount(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRegisterPatient(t *testing.T) {
	svc, pool := setup(t)

	u, err := svc.RegisterPatient(context.Background(), input("  Alice_1 ", "Alice@Example.com"))
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	if u.Username != "alice_1" {
		t.Errorf("username = %q, want alice_1", u.Username)
	}
	if u.Email != "alice@example.com" {
		t.Errorf("email = %q, want alice@example.com", u.Email)
	}
	if !password.Matches(u.PasswordHash, testPassword) {
		t.Error("stored hash does not match the password")
	}
	if n := rowCount(t, pool, "SELECT count(*) FROM patients WHERE patient_id = $1", u.UserID); n != 1 {
		t.Errorf("patient rows = %d, want 1", n)
	}
	if n := rowCount(t, pool, "SELECT count(*) FROM doctors WHERE doctor_id = $1", u.UserID); n != 0 {
		t.Errorf("doctor rows = %d, want 0", n)
	}
}

func TestRegisterDoctor(t *testing.T) {
	svc, pool := setup(t)

	u, err := svc.RegisterDoctor(context.Background(), accounts.DoctorInput{
		UserInfo: input("dr_bob", "bob@example.com"),
		IsAdmin:  true,
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	if n := rowCount(t, pool,
		"SELECT count(*) FROM doctors WHERE doctor_id = $1 AND is_admin", u.UserID); n != 1 {
		t.Errorf("admin doctor rows = %d, want 1", n)
	}
	if n := rowCount(t, pool, "SELECT count(*) FROM patients WHERE patient_id = $1", u.UserID); n != 0 {
		t.Errorf("patient rows = %d, want 0", n)
	}
}

func TestRegisterDuplicateUsername(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()

	if _, err := svc.RegisterPatient(ctx, input("alice", "a1@example.com")); err != nil {
		t.Fatal(err)
	}
	// same username with different case, different email
	_, err := svc.RegisterPatient(ctx, input("ALICE", "a2@example.com"))
	if !errors.Is(err, accounts.ErrUsernameTaken) {
		t.Fatalf("err = %v, want ErrUsernameTaken", err)
	}
	if n := rowCount(t, pool, "SELECT count(*) FROM users"); n != 1 {
		t.Errorf("users = %d, want 1", n)
	}
}

func TestRegisterDuplicateEmail(t *testing.T) {
	svc, _ := setup(t)
	ctx := context.Background()

	if _, err := svc.RegisterPatient(ctx, input("alice", "a@example.com")); err != nil {
		t.Fatal(err)
	}
	_, err := svc.RegisterPatient(ctx, input("alice2", "A@Example.com"))
	if !errors.Is(err, accounts.ErrEmailTaken) {
		t.Fatalf("err = %v, want ErrEmailTaken", err)
	}
}

func TestRegisterValidation(t *testing.T) {
	svc, pool := setup(t)

	in := input("ab", "not-an-email")
	in.Password = "short"
	_, err := svc.RegisterPatient(context.Background(), in)

	var ve *apperr.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want *ValidationError", err)
	}
	for _, field := range []string{"username", "email", "password"} {
		if _, ok := ve.Problems[field]; !ok {
			t.Errorf("no problem reported for %q", field)
		}
	}
	if n := rowCount(t, pool, "SELECT count(*) FROM users"); n != 0 {
		t.Errorf("users = %d, want 0", n)
	}
}
