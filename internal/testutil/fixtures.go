package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"clinic/internal/store"
)

func newUser(t *testing.T, q *store.Queries, name string) int32 {
	t.Helper()
	u, err := q.CreateUser(context.Background(), store.CreateUserParams{
		Username: name, Email: name + "@example.com", PasswordHash: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	return u.UserID
}

func NewDoctor(t *testing.T, pool *pgxpool.Pool, name string) int32 {
	t.Helper()
	q := store.New(pool)
	id := newUser(t, q, name)
	if err := q.CreateDoctor(context.Background(), store.CreateDoctorParams{DoctorID: id}); err != nil {
		t.Fatal(err)
	}
	return id
}

func NewPatient(t *testing.T, pool *pgxpool.Pool, name string) int32 {
	t.Helper()
	q := store.New(pool)
	id := newUser(t, q, name)
	if err := q.CreatePatient(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	return id
}

// NextDay returns local midnight of the first given weekday at least 7 days from now,
// so anything booked on it is always in the future.
func NextDay(loc *time.Location, weekday time.Weekday) time.Time {
	d := time.Now().In(loc).AddDate(0, 0, 7)
	for d.Weekday() != weekday {
		d = d.AddDate(0, 0, 1)
	}
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
}
