package booking_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"clinic/internal/apperr"
	"clinic/internal/auth"
	"clinic/internal/booking"
	"clinic/internal/scheduling"
	"clinic/internal/store"
	"clinic/internal/testutil"
)

func startOf(t *testing.T, pool *pgxpool.Pool, id int32) time.Time {
	t.Helper()
	var s time.Time
	err := pool.QueryRow(context.Background(),
		"SELECT appointment_start FROM appointments WHERE appointment_id = $1", id).Scan(&s)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// slotOffset returns ten distinct slot offsets inside the 09:00-17:00 shift with a 12:00-13:00 break.
func slotOffset(i int) time.Duration {
	if i < 6 {
		return 9*time.Hour + time.Duration(i)*30*time.Minute
	}
	return 13*time.Hour + time.Duration(i-6)*30*time.Minute
}

func TestReschedule(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	doc := testutil.NewDoctor(t, pool, "doc")
	addShift(t, pool, doc)
	pat := patient(t, pool, "pat")
	mon := monday()
	old, next := mon.Add(10*time.Hour), mon.Add(14*time.Hour)

	appt, err := svc.Book(ctx, pat, doc, old)
	if err != nil {
		t.Fatal(err)
	}

	moved, err := svc.Reschedule(ctx, pat, appt.AppointmentID, next)
	if err != nil {
		t.Fatalf("reschedule: %v", err)
	}
	if moved.AppointmentID != appt.AppointmentID || !moved.AppointmentStart.Equal(next) ||
		moved.Status != store.AppointmentStatusBooked {
		t.Errorf("moved = %+v", moved)
	}

	slots, err := svc.FreeSlots(ctx, doc, mon)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(slots, old.Equal) {
		t.Error("old slot is not free again")
	}
	if slices.ContainsFunc(slots, next.Equal) {
		t.Error("new slot is still free")
	}

	// the freed slot can be booked by someone else, and the patient holds exactly one booking
	if _, err := svc.Book(ctx, patient(t, pool, "other"), doc, old); err != nil {
		t.Errorf("book freed slot: %v", err)
	}
	var n int
	err = pool.QueryRow(ctx,
		"SELECT count(*) FROM appointments WHERE patient_id = $1 AND status = 'booked'", pat.UserID).Scan(&n)
	if err != nil || n != 1 {
		t.Errorf("patient bookings = (%d, %v), want 1", n, err)
	}
}

func TestRescheduleValidatesNewSlot(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	doc := testutil.NewDoctor(t, pool, "doc")
	addShift(t, pool, doc)
	pat := patient(t, pool, "pat")
	mon := monday()

	_, err := pool.Exec(ctx,
		"INSERT INTO doctor_time_off (doctor_id, start_date, end_date) VALUES ($1, $2, $2)",
		doc, mon.AddDate(0, 0, 7))
	if err != nil {
		t.Fatal(err)
	}

	original := mon.Add(10 * time.Hour)
	appt, err := svc.Book(ctx, pat, doc, original)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		start time.Time
	}{
		{"before the shift", mon.Add(8 * time.Hour)},
		{"inside the break", mon.Add(12*time.Hour + 30*time.Minute)},
		{"at shift end", mon.Add(17 * time.Hour)},
		{"off the half hour", mon.Add(9*time.Hour + 15*time.Minute)},
		{"day without a shift", mon.AddDate(0, 0, 1).Add(10 * time.Hour)},
		{"day on leave", mon.AddDate(0, 0, 7).Add(10 * time.Hour)},
		{"in the past", mon.AddDate(0, 0, -14).Add(10 * time.Hour)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := svc.Reschedule(ctx, pat, appt.AppointmentID, c.start); !errors.Is(err, booking.ErrSlotUnavailable) {
				t.Fatalf("err = %v, want ErrSlotUnavailable", err)
			}
			if got := startOf(t, pool, appt.AppointmentID); !got.Equal(original) {
				t.Errorf("appointment moved to %v despite the error", got)
			}
		})
	}
}

func TestRescheduleTakenAndBusy(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	d1, d2 := testutil.NewDoctor(t, pool, "d1"), testutil.NewDoctor(t, pool, "d2")
	addShift(t, pool, d1)
	addShift(t, pool, d2)
	a, b := patient(t, pool, "a"), patient(t, pool, "b")
	mon := monday()
	ten, two := mon.Add(10*time.Hour), mon.Add(14*time.Hour)

	if _, err := svc.Book(ctx, a, d1, ten); err != nil {
		t.Fatal(err)
	}
	bAppt, err := svc.Book(ctx, b, d1, two)
	if err != nil {
		t.Fatal(err)
	}

	// the doctor's slot is held by another patient
	if _, err := svc.Reschedule(ctx, b, bAppt.AppointmentID, ten); !errors.Is(err, booking.ErrSlotTaken) {
		t.Fatalf("err = %v, want ErrSlotTaken", err)
	}
	if got := startOf(t, pool, bAppt.AppointmentID); !got.Equal(two) {
		t.Errorf("appointment moved to %v despite the error", got)
	}

	// the new time collides with the patient's own booking elsewhere
	if _, err := svc.Book(ctx, b, d2, mon.Add(11*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Reschedule(ctx, b, bAppt.AppointmentID, mon.Add(11*time.Hour)); !errors.Is(err, booking.ErrPatientBusy) {
		t.Fatalf("err = %v, want ErrPatientBusy", err)
	}
	if got := startOf(t, pool, bAppt.AppointmentID); !got.Equal(two) {
		t.Errorf("appointment moved to %v despite the error", got)
	}
}

func TestRescheduleStateErrors(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	doc := testutil.NewDoctor(t, pool, "doc")
	addShift(t, pool, doc)
	a, b := patient(t, pool, "a"), patient(t, pool, "b")
	mon := monday()
	original, target := mon.Add(10*time.Hour), mon.Add(14*time.Hour)

	appt, err := svc.Book(ctx, a, doc, original)
	if err != nil {
		t.Fatal(err)
	}
	id := appt.AppointmentID

	if _, err := svc.Reschedule(ctx, b, id, target); !errors.Is(err, booking.ErrNotFound) {
		t.Errorf("someone else's: err = %v, want ErrNotFound", err)
	}
	if _, err := svc.Reschedule(ctx, a, 9999, target); !errors.Is(err, booking.ErrNotFound) {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}
	asDoctor := auth.Principal{UserID: doc, Username: "doc", Role: auth.RoleDoctor}
	if _, err := svc.Reschedule(ctx, asDoctor, id, target); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("doctor: err = %v, want ErrForbidden", err)
	}

	var ve *apperr.ValidationError
	if _, err := svc.Reschedule(ctx, a, id, original); !errors.As(err, &ve) {
		t.Errorf("same time: err = %v, want ValidationError", err)
	}

	// cancelled appointments can't be moved
	if err := svc.Cancel(ctx, a, id, reason); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Reschedule(ctx, a, id, target); !errors.Is(err, booking.ErrAlreadyCancelled) {
		t.Errorf("cancelled: err = %v, want ErrAlreadyCancelled", err)
	}
	if got := startOf(t, pool, id); !got.Equal(original) {
		t.Errorf("cancelled appointment moved to %v", got)
	}

	// appointments in the past can't be moved
	var pastID int32
	past := time.Now().UTC().Truncate(time.Hour).AddDate(0, 0, -1)
	err = pool.QueryRow(ctx, `INSERT INTO appointments (doctor_id, patient_id, appointment_start)
		VALUES ($1, $2, $3) RETURNING appointment_id`, doc, a.UserID, past).Scan(&pastID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Reschedule(ctx, a, pastID, target); !errors.Is(err, booking.ErrNotReschedulable) {
		t.Errorf("past: err = %v, want ErrNotReschedulable", err)
	}
}

// Ten patients move their own appointments to the same free slot: exactly one wins,
// and every loser keeps the appointment they had.
func TestConcurrentRescheduleSameSlot(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	doc := testutil.NewDoctor(t, pool, "doc")
	addShift(t, pool, doc)
	mon := monday()
	target := mon.Add(16 * time.Hour)

	const n = 10
	patients := make([]auth.Principal, n)
	ids := make([]int32, n)
	for i := range patients {
		patients[i] = patient(t, pool, fmt.Sprintf("pat%d", i))
		a, err := svc.Book(ctx, patients[i], doc, mon.Add(slotOffset(i)))
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = a.AppointmentID
	}

	errs := make([]error, n)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-release
			_, errs[i] = svc.Reschedule(ctx, patients[i], ids[i], target)
		}(i)
	}
	close(release)
	wg.Wait()

	var won, taken int
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, booking.ErrSlotTaken):
			taken++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if won != 1 || taken != n-1 {
		t.Fatalf("won = %d, taken = %d, want 1 and %d", won, taken, n-1)
	}
	if c := bookedCount(t, pool, doc); c != n {
		t.Errorf("booked rows = %d, want %d (no appointment may be lost)", c, n)
	}
	var atTarget int
	err := pool.QueryRow(ctx,
		"SELECT count(*) FROM appointments WHERE doctor_id = $1 AND status = 'booked' AND appointment_start = $2",
		doc, target).Scan(&atTarget)
	if err != nil || atTarget != 1 {
		t.Errorf("rows at target = (%d, %v), want 1", atTarget, err)
	}
}

// A reschedule and a cancel of the same appointment race. Cancel always succeeds;
// the reschedule either lands first or sees the cancellation. Nothing stays booked.
func TestRescheduleRacesCancel(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	doc := testutil.NewDoctor(t, pool, "doc")
	addShift(t, pool, doc)
	pat := patient(t, pool, "pat")
	mon := monday()

	for i := 0; i < 20; i++ {
		if _, err := pool.Exec(ctx, "DELETE FROM appointments"); err != nil {
			t.Fatal(err)
		}
		appt, err := svc.Book(ctx, pat, doc, mon.Add(10*time.Hour))
		if err != nil {
			t.Fatal(err)
		}

		var resErr, cancelErr error
		release := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-release
			_, resErr = svc.Reschedule(ctx, pat, appt.AppointmentID, mon.Add(14*time.Hour))
		}()
		go func() {
			defer wg.Done()
			<-release
			cancelErr = svc.Cancel(ctx, pat, appt.AppointmentID, reason)
		}()
		close(release)
		wg.Wait()

		if cancelErr != nil {
			t.Fatalf("iteration %d: cancel err = %v", i, cancelErr)
		}
		if resErr != nil && !errors.Is(resErr, booking.ErrAlreadyCancelled) {
			t.Fatalf("iteration %d: reschedule err = %v", i, resErr)
		}
		if c := bookedCount(t, pool, doc); c != 0 {
			t.Fatalf("iteration %d: %d booked rows remain, want 0", i, c)
		}
	}
}

// A reschedule and a schedule change that would strand the new time race each other.
// Exactly one succeeds; both succeeding would leave an appointment outside working hours.
func TestRescheduleRacesScheduleChange(t *testing.T) {
	pool := testutil.NewPool(t)
	st := store.NewStore(pool)
	bk := booking.NewService(st, eat)
	sc := scheduling.NewService(st, eat)
	ctx := context.Background()

	doc := testutil.NewDoctor(t, pool, "doc")
	pat := patient(t, pool, "pat")
	mon := monday()
	open := scheduling.Shift{Weekday: 1, Start: "09:00", End: "17:00"}
	late := scheduling.Shift{Weekday: 1, Start: "11:00", End: "17:00"} // excludes 10:00

	for i := 0; i < 30; i++ {
		if _, err := pool.Exec(ctx, "DELETE FROM appointments"); err != nil {
			t.Fatal(err)
		}
		if _, err := sc.SetShift(ctx, doc, open); err != nil {
			t.Fatalf("iteration %d reset: %v", i, err)
		}
		appt, err := bk.Book(ctx, pat, doc, mon.Add(14*time.Hour))
		if err != nil {
			t.Fatalf("iteration %d book: %v", i, err)
		}

		var resErr, setErr error
		release := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-release
			_, resErr = bk.Reschedule(ctx, pat, appt.AppointmentID, mon.Add(10*time.Hour))
		}()
		go func() {
			defer wg.Done()
			<-release
			_, setErr = sc.SetShift(ctx, doc, late)
		}()
		close(release)
		wg.Wait()

		switch {
		case resErr == nil && errors.Is(setErr, scheduling.ErrConflictingAppointments):
		case setErr == nil && errors.Is(resErr, booking.ErrSlotUnavailable):
		default:
			t.Fatalf("iteration %d: reschedule err = %v, set err = %v; want exactly one to succeed", i, resErr, setErr)
		}
	}
}
