package auth_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"clinic/internal/accounts"
	"clinic/internal/auth"
	"clinic/internal/store"
	"clinic/internal/testutil"
)

const testPassword = "correct-horse-battery"

// setup returns the auth service, the accounts service (used to create test users), and the pool.
func setup(t *testing.T) (*auth.Service, *accounts.Service, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.NewPool(t)
	st := store.NewStore(pool)
	tokens := auth.NewTokenManager(testSecret, 15*time.Minute)
	return auth.NewService(st, tokens), accounts.NewService(st), pool
}

func input(username, email string) accounts.RegisterInput {
	return accounts.RegisterInput{
		Username: username, Email: email, Password: testPassword,
		Firstname: "Test", Lastname: "User",
	}
}
