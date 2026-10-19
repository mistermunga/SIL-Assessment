package booking_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"clinic/internal/apperr"
	"clinic/internal/auth"
	"clinic/internal/booking"
	"clinic/internal/store"
	"clinic/internal/testutil"
)

func doctorPrincipal(id int32, name string) auth.Principal {
	return auth.Principal{UserID: id, Username: name, Role: auth.RoleDoctor}
}

// pastAppt inserts an appointment that started daysAgo days ago with the given status.
func pastAppt(t *testing.T, pool *pgxpool.Pool, doctorID, patientID int32, daysAgo int, status string) int32 {
	t.Helper()
	start := time.Now().UTC().Truncate(time.Hour).AddDate(0, 0, -daysAgo)
	var id int32
	err := pool.QueryRow(context.Background(), `
		INSERT INTO appointments (doctor_id, patient_id, appointment_start, status)
		VALUES ($1, $2, $3, $4::appointment_status) RETURNING appointment_id`,
		doctorID, patientID, start, status).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func statusAndNotes(t *testing.T, pool *pgxpool.Pool, id int32) (string, *string) {
	t.Helper()
	var status string
	var notes *string
	err := pool.QueryRow(context.Background(),
		"SELECT status::text, notes FROM appointments WHERE appointment_id = $1", id).Scan(&status, &notes)
	if err != nil {
		t.Fatal(err)
	}
	return status, notes
}

func TestMarkOutcome(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	doc := testutil.NewDoctor(t, pool, "doc")
	pat := patient(t, pool, "pat")
	asDoc := doctorPrincipal(doc, "doc")

	seen := pastAppt(t, pool, doc, pat.UserID, 1, "booked")
	missed := pastAppt(t, pool, doc, pat.UserID, 2, "booked")

	if err := svc.MarkOutcome(ctx, asDoc, seen, store.AppointmentStatusCompleted, "  follow up in 2 weeks "); err != nil {
		t.Fatalf("completed: %v", err)
	}
	if err := svc.MarkOutcome(ctx, asDoc, missed, store.AppointmentStatusNoShow, ""); err != nil {
		t.Fatalf("no_show: %v", err)
	}

	status, notes := statusAndNotes(t, pool, seen)
	if status != "completed" || notes == nil || *notes != "follow up in 2 weeks" {
		t.Errorf("seen = (%s, %v), want completed with trimmed notes", status, notes)
	}
	status, notes = statusAndNotes(t, pool, missed)
	if status != "no_show" || notes != nil {
		t.Errorf("missed = (%s, %v), want no_show with no notes", status, notes)
	}
}

func TestMarkOutcomeRejections(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	doc, other := testutil.NewDoctor(t, pool, "doc"), testutil.NewDoctor(t, pool, "doc2")
	addShift(t, pool, doc)
	pat := patient(t, pool, "pat")
	asDoc := doctorPrincipal(doc, "doc")

	open := pastAppt(t, pool, doc, pat.UserID, 1, "booked")
	cancelled := pastAppt(t, pool, doc, pat.UserID, 2, "cancelled")
	done := pastAppt(t, pool, doc, pat.UserID, 3, "completed")
	future, err := svc.Book(ctx, pat, doc, monday().Add(10*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	// who may call
	if err := svc.MarkOutcome(ctx, pat, open, store.AppointmentStatusCompleted, ""); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("patient: err = %v, want ErrForbidden", err)
	}
	if err := svc.MarkOutcome(ctx, doctorPrincipal(other, "doc2"), open, store.AppointmentStatusCompleted, ""); !errors.Is(err, booking.ErrNotFound) {
		t.Errorf("another doctor's: err = %v, want ErrNotFound", err)
	}
	if err := svc.MarkOutcome(ctx, asDoc, 9999, store.AppointmentStatusCompleted, ""); !errors.Is(err, booking.ErrNotFound) {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}

	// input
	for name, c := range map[string]struct {
		outcome store.AppointmentStatus
		notes   string
	}{
		"booked as outcome":    {store.AppointmentStatusBooked, ""},
		"cancelled as outcome": {store.AppointmentStatusCancelled, ""},
		"notes too long":       {store.AppointmentStatusCompleted, strings.Repeat("x", 501)},
	} {
		var ve *apperr.ValidationError
		if err := svc.MarkOutcome(ctx, asDoc, open, c.outcome, c.notes); !errors.As(err, &ve) {
			t.Errorf("%s: err = %v, want ValidationError", name, err)
		}
	}

	// state
	for name, id := range map[string]int32{
		"not started yet":   future.AppointmentID,
		"cancelled":         cancelled,
		"already completed": done,
	} {
		if err := svc.MarkOutcome(ctx, asDoc, id, store.AppointmentStatusNoShow, ""); !errors.Is(err, booking.ErrNotMarkable) {
			t.Errorf("%s: err = %v, want ErrNotMarkable", name, err)
		}
	}

	// nothing above changed any appointment
	for id, want := range map[int32]string{
		open: "booked", cancelled: "cancelled", done: "completed", future.AppointmentID: "booked",
	} {
		if got, _ := statusAndNotes(t, pool, id); got != want {
			t.Errorf("appointment %d status = %s, want %s", id, got, want)
		}
	}
}

// Many simultaneous attempts to record an outcome: exactly one lands.
func TestConcurrentMarkOutcome(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	doc := testutil.NewDoctor(t, pool, "doc")
	pat := patient(t, pool, "pat")
	asDoc := doctorPrincipal(doc, "doc")
	id := pastAppt(t, pool, doc, pat.UserID, 1, "booked")

	const n = 10
	errs := make([]error, n)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outcome := store.AppointmentStatusCompleted
			if i%2 == 1 {
				outcome = store.AppointmentStatusNoShow
			}
			<-release
			errs[i] = svc.MarkOutcome(ctx, asDoc, id, outcome, "")
		}(i)
	}
	close(release)
	wg.Wait()

	var won, rejected int
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, booking.ErrNotMarkable):
			rejected++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if won != 1 || rejected != n-1 {
		t.Fatalf("won = %d, rejected = %d, want 1 and %d", won, rejected, n-1)
	}
	if status, _ := statusAndNotes(t, pool, id); status != "completed" && status != "no_show" {
		t.Errorf("final status = %s", status)
	}
}

func TestPendingOutcomes(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	doc, other := testutil.NewDoctor(t, pool, "doc"), testutil.NewDoctor(t, pool, "doc2")
	addShift(t, pool, doc)
	p1, p2 := patient(t, pool, "p1"), patient(t, pool, "p2")
	asDoc := doctorPrincipal(doc, "doc")

	older := pastAppt(t, pool, doc, p1.UserID, 3, "booked")
	newer := pastAppt(t, pool, doc, p1.UserID, 1, "booked")
	pastAppt(t, pool, doc, p1.UserID, 2, "completed")
	pastAppt(t, pool, doc, p1.UserID, 4, "cancelled")
	pastAppt(t, pool, other, p2.UserID, 1, "booked")                              // another doctor's
	if _, err := svc.Book(ctx, p2, doc, monday().Add(10*time.Hour)); err != nil { // future, not pending
		t.Fatal(err)
	}

	list, err := svc.PendingOutcomes(ctx, asDoc, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int32
	for _, a := range list {
		ids = append(ids, a.ID)
	}
	if want := []int32{older, newer}; !slices.Equal(ids, want) {
		t.Fatalf("pending = %v, want %v (oldest first)", ids, want)
	}

	first, err := svc.PendingOutcomes(ctx, asDoc, 1)
	if err != nil || len(first) != 1 || first[0].ID != older {
		t.Errorf("limit 1 = (%v, %v), want only the oldest", first, err)
	}

	// recording an outcome removes it from the list
	if err := svc.MarkOutcome(ctx, asDoc, older, store.AppointmentStatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
	list, _ = svc.PendingOutcomes(ctx, asDoc, 0)
	if len(list) != 1 || list[0].ID != newer {
		t.Errorf("after marking: %v, want only %d", list, newer)
	}

	if _, err := svc.PendingOutcomes(ctx, p1, 0); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("patient: err = %v, want ErrForbidden", err)
	}
}
