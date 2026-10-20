package accounts_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"clinic/internal/accounts"
	"clinic/internal/apperr"
)

func adminCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	return rowCount(t, pool, "SELECT count(*) FROM doctors WHERE is_admin")
}

func TestEnsureAdminCreatesOnce(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	in := input("administrator", "admin@clinic.local")

	created, err := svc.EnsureAdmin(ctx, in)
	if err != nil || !created {
		t.Fatalf("first call = (%v, %v), want (true, nil)", created, err)
	}
	if n := adminCount(t, pool); n != 1 {
		t.Fatalf("admins = %d, want 1", n)
	}

	created, err = svc.EnsureAdmin(ctx, in)
	if err != nil || created {
		t.Fatalf("second call = (%v, %v), want (false, nil)", created, err)
	}
	if n := rowCount(t, pool, "SELECT count(*) FROM users"); n != 1 {
		t.Errorf("users = %d, want 1", n)
	}
}

func TestEnsureAdminSkipsWhenAnotherAdminExists(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()

	if _, err := svc.RegisterDoctor(ctx, accounts.DoctorInput{
		UserInfo: input("boss", "boss@example.com"), IsAdmin: true,
	}); err != nil {
		t.Fatal(err)
	}

	created, err := svc.EnsureAdmin(ctx, input("administrator", "admin@clinic.local"))
	if err != nil || created {
		t.Fatalf("= (%v, %v), want (false, nil)", created, err)
	}
	if n := rowCount(t, pool, "SELECT count(*) FROM users"); n != 1 {
		t.Errorf("users = %d, want 1", n)
	}
}

func TestEnsureAdminUsernameHeldByAPatient(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()

	if _, err := svc.RegisterPatient(ctx, input("administrator", "p@example.com")); err != nil {
		t.Fatal(err)
	}
	created, err := svc.EnsureAdmin(ctx, input("administrator", "admin@clinic.local"))
	if created || !errors.Is(err, accounts.ErrUsernameTaken) {
		t.Fatalf("= (%v, %v), want (false, ErrUsernameTaken)", created, err)
	}
	if n := adminCount(t, pool); n != 0 {
		t.Errorf("admins = %d, want 0", n)
	}
}

func TestEnsureAdminValidatesCredentials(t *testing.T) {
	svc, pool := setup(t)

	in := input("administrator", "admin@clinic.local")
	in.Password = "short"
	created, err := svc.EnsureAdmin(context.Background(), in)

	var ve *apperr.ValidationError
	if created || !errors.As(err, &ve) {
		t.Fatalf("= (%v, %v), want (false, ValidationError)", created, err)
	}
	if n := rowCount(t, pool, "SELECT count(*) FROM users"); n != 0 {
		t.Errorf("users = %d, want 0", n)
	}
}

// Several instances starting together: exactly one creates the admin, none fails.
func TestEnsureAdminConcurrentStartups(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	in := input("administrator", "admin@clinic.local")

	const n = 5
	created := make([]bool, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			created[i], errs[i] = svc.EnsureAdmin(ctx, in)
		}(i)
	}
	wg.Wait()

	made := 0
	for i := range created {
		if errs[i] != nil {
			t.Errorf("instance %d: %v", i, errs[i])
		}
		if created[i] {
			made++
		}
	}
	if made != 1 {
		t.Errorf("%d instances created the admin, want 1", made)
	}
	if c := adminCount(t, pool); c != 1 {
		t.Errorf("admins = %d, want 1", c)
	}
}
