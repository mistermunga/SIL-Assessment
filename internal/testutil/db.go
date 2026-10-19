package testutil

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool connects to TEST_DATABASE_URL and empties every table.
// Locally the test is skipped when the variable is unset; in CI it fails.
func NewPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL is required in CI")
		}
		t.Skip("TEST_DATABASE_URL not set")
	}

	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 25
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	if db := pool.Config().ConnConfig.Database; !strings.Contains(db, "test") {
		t.Fatalf("refusing to truncate %q: database name must contain \"test\"", db)
	}

	_, err = pool.Exec(ctx, `TRUNCATE appointments, doctor_time_off, doctor_schedules,
		patients, doctors, users RESTART IDENTITY`)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}
