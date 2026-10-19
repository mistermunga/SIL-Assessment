package booking_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
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

const reason = "schedule conflict"

var eat = time.FixedZone("EAT", 3*60*60)

func setup(t *testing.T) (*booking.Service, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.NewPool(t)
	return booking.NewService(store.NewStore(pool), eat), pool
}

func patient(t *testing.T, pool *pgxpool.Pool, name string) auth.Principal {
	t.Helper()
	return auth.Principal{UserID: testutil.NewPatient(t, pool, name), Username: name, Role: auth.RolePatient}
}

func monday() time.Time { return testutil.NextDay(eat, time.Monday) }

// addShift gives the doctor a Monday shift of 09:00-17:00 with a 12:00-13:00 break.
func addShift(t *testing.T, pool *pgxpool.Pool, doctorID int32) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO doctor_schedules (doctor_id, weekday, start_time, end_time, break_start, break_end)
		VALUES ($1, 1, '09:00', '17:00', '12:00', '13:00')`, doctorID)
	if err != nil {
		t.Fatal(err)
	}
}

func bookedCount(t *testing.T, pool *pgxpool.Pool, doctorID int32) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM appointments WHERE doctor_id = $1 AND status = 'booked'", doctorID).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// ---- booking ----

func TestBook(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	doc := testutil.NewDoctor(t, pool, "doc")
	addShift(t, pool, doc)
	pat := patient(t, pool, "pat")
	slot := monday().Add(10 * time.Hour)

	a, err := svc.Book(ctx, pat, doc, slot)
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	if a.DoctorID != doc || a.PatientID != pat.UserID || !a.AppointmentStart.Equal(slot) ||
		a.Status != store.AppointmentStatusBooked {
		t.Errorf("appointment = %+v", a)
	}

	// the slots touching the break are bookable
	for _, d := range []time.Duration{11*time.Hour + 30*time.Minute, 13 * time.Hour} {
		if _, err := svc.Book(ctx, pat, doc, monday().Add(d)); err != nil {
			t.Errorf("book at +%v: %v", d, err)
		}
	}
}

func TestBookUnavailable(t *testing.T) {
	svc, pool := setup(t)
	doc := testutil.NewDoctor(t, pool, "doc")
	addShift(t, pool, doc)
	pat := patient(t, pool, "pat")
	mon := monday()

	cases := []struct {
		name  string
		start time.Time
	}{
		{"before the shift", mon.Add(8 * time.Hour)},
		{"start of the break", mon.Add(12 * time.Hour)},
		{"inside the break", mon.Add(12*time.Hour + 30*time.Minute)},
		{"at shift end", mon.Add(17 * time.Hour)},
		{"off the half hour", mon.Add(9*time.Hour + 15*time.Minute)},
		{"with seconds", mon.Add(9*time.Hour + 30*time.Second)},
		{"day without a shift", mon.AddDate(0, 0, 1).Add(10 * time.Hour)},
		{"in the past", mon.AddDate(0, 0, -14).Add(10 * time.Hour)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := svc.Book(context.Background(), pat, doc, c.start)
			if !errors.Is(err, booking.ErrSlotUnavailable) {
				t.Fatalf("err = %v, want ErrSlotUnavailable", err)
			}
		})
	}
	if n := bookedCount(t, pool, doc); n != 0 {
		t.Errorf("booked rows = %d, want 0", n)
	}
}

func TestBookOnLeave(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	doc := testutil.NewDoctor(t, pool, "doc")
	addShift(t, pool, doc)
	pat := patient(t, pool, "pat")
	mon := monday()

	_, err := pool.Exec(ctx,
		"INSERT INTO doctor_time_off (doctor_id, start_date, end_date) VALUES ($1, $2, $2)", doc, mon)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Book(ctx, pat, doc, mon.Add(10*time.Hour)); !errors.Is(err, booking.ErrSlotUnavailable) {
		t.Fatalf("err = %v, want ErrSlotUnavailable", err)
	}
	// the following Monday is not covered
	if _, err := svc.Book(ctx, pat, doc, mon.AddDate(0, 0, 7).Add(10*time.Hour)); err != nil {
		t.Errorf("week after leave: %v", err)
	}
}

func TestBookRejectsCallerAndDoctor(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	doc := testutil.NewDoctor(t, pool, "doc")
	addShift(t, pool, doc)
	slot := monday().Add(10 * time.Hour)

	asDoctor := auth.Principal{UserID: doc, Username: "doc", Role: auth.RoleDoctor}
	if _, err := svc.Book(ctx, asDoctor, doc, slot); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("doctor booking: err = %v, want ErrForbidden", err)
	}

	pat := patient(t, pool, "pat")
	if _, err := svc.Book(ctx, pat, 9999, slot); !errors.Is(err, scheduling.ErrDoctorNotFound) {
		t.Errorf("unknown doctor: err = %v, want ErrDoctorNotFound", err)
	}

	if _, err := pool.Exec(ctx, "UPDATE users SET deleted_at = now() WHERE user_id = $1", doc); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Book(ctx, pat, doc, slot); !errors.Is(err, scheduling.ErrDoctorNotFound) {
		t.Errorf("deleted doctor: err = %v, want ErrDoctorNotFound", err)
	}
}

func TestBookTakenThenFreedByCancel(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	doc := testutil.NewDoctor(t, pool, "doc")
	addShift(t, pool, doc)
	a, b := patient(t, pool, "a"), patient(t, pool, "b")
	slot := monday().Add(10 * time.Hour)

	appt, err := svc.Book(ctx, a, doc, slot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Book(ctx, b, doc, slot); !errors.Is(err, booking.ErrSlotTaken) {
		t.Fatalf("err = %v, want ErrSlotTaken", err)
	}

	if err := svc.Cancel(ctx, a, appt.AppointmentID, reason); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Book(ctx, b, doc, slot); err != nil {
		t.Errorf("rebook after cancel: %v", err)
	}
}

func TestBookPatientCannotHoldTwoAtOnce(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	d1, d2 := testutil.NewDoctor(t, pool, "d1"), testutil.NewDoctor(t, pool, "d2")
	addShift(t, pool, d1)
	addShift(t, pool, d2)
	pat := patient(t, pool, "pat")
	ten := monday().Add(10 * time.Hour)

	first, err := svc.Book(ctx, pat, d1, ten)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Book(ctx, pat, d2, ten); !errors.Is(err, booking.ErrPatientBusy) {
		t.Fatalf("same time, other doctor: err = %v, want ErrPatientBusy", err)
	}
	if _, err := svc.Book(ctx, pat, d2, ten.Add(30*time.Minute)); err != nil {
		t.Errorf("adjacent slot: %v", err)
	}

	// cancelling frees the patient's time
	if err := svc.Cancel(ctx, pat, first.AppointmentID, reason); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Book(ctx, pat, d2, ten); err != nil {
		t.Errorf("after cancel: %v", err)
	}
}

// ---- cancelling ----

func TestCancel(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	doc := testutil.NewDoctor(t, pool, "doc")
	addShift(t, pool, doc)
	a, b := patient(t, pool, "a"), patient(t, pool, "b")

	appt, err := svc.Book(ctx, a, doc, monday().Add(10*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	id := appt.AppointmentID

	if err := svc.Cancel(ctx, b, id, reason); !errors.Is(err, booking.ErrNotFound) {
		t.Errorf("someone else's: err = %v, want ErrNotFound", err)
	}
	if err := svc.Cancel(ctx, a, 9999, reason); !errors.Is(err, booking.ErrNotFound) {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}

	if err := svc.Cancel(ctx, a, id, "  "+reason+"  "); err != nil { // padding is trimmed
		t.Fatalf("cancel: %v", err)
	}
	var notes string
	if err := pool.QueryRow(ctx, "SELECT notes FROM appointments WHERE appointment_id = $1", id).Scan(&notes); err != nil {
		t.Fatal(err)
	}
	if notes != reason {
		t.Errorf("notes = %q, want %q", notes, reason)
	}

	// second cancel: dedicated error, original reason untouched
	if err := svc.Cancel(ctx, a, id, "a different reason"); !errors.Is(err, booking.ErrAlreadyCancelled) {
		t.Errorf("second cancel: err = %v, want ErrAlreadyCancelled", err)
	}
	if err := pool.QueryRow(ctx, "SELECT notes FROM appointments WHERE appointment_id = $1", id).Scan(&notes); err != nil {
		t.Fatal(err)
	}
	if notes != reason {
		t.Errorf("notes overwritten: %q", notes)
	}
}

func TestCancelRequiresReason(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	doc := testutil.NewDoctor(t, pool, "doc")
	addShift(t, pool, doc)
	pat := patient(t, pool, "pat")

	appt, err := svc.Book(ctx, pat, doc, monday().Add(10*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	for name, r := range map[string]string{
		"empty":      "",
		"whitespace": "   \t ",
		"too long":   strings.Repeat("x", 501),
	} {
		err := svc.Cancel(ctx, pat, appt.AppointmentID, r)
		var ve *apperr.ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: err = %v, want ValidationError", name, err)
			continue
		}
		if _, ok := ve.Problems["reason"]; !ok {
			t.Errorf("%s: problems = %v, want a message for reason", name, ve.Problems)
		}
	}
	if n := bookedCount(t, pool, doc); n != 1 {
		t.Errorf("booked rows = %d, want 1 (a rejected cancel must change nothing)", n)
	}
}

func TestCancelPastAppointment(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	doc := testutil.NewDoctor(t, pool, "doc")
	pat := patient(t, pool, "pat")

	past := time.Now().UTC().Truncate(time.Hour).AddDate(0, 0, -1)
	var id int32
	err := pool.QueryRow(ctx, `INSERT INTO appointments (doctor_id, patient_id, appointment_start)
		VALUES ($1, $2, $3) RETURNING appointment_id`, doc, pat.UserID, past).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.Cancel(ctx, pat, id, reason); !errors.Is(err, booking.ErrNotCancellable) {
		t.Fatalf("err = %v, want ErrNotCancellable", err)
	}
}

// ---- free slots ----

func TestFreeSlots(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	doc := testutil.NewDoctor(t, pool, "doc")
	addShift(t, pool, doc)
	pat := patient(t, pool, "pat")
	mon := monday()
	ten := mon.Add(10 * time.Hour)

	slots, err := svc.FreeSlots(ctx, doc, mon)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 14 { // 16 half-hours in the shift, minus 2 in the break
		t.Fatalf("slots = %d, want 14", len(slots))
	}
	if !slots[0].Equal(mon.Add(9*time.Hour)) || !slots[13].Equal(mon.Add(16*time.Hour+30*time.Minute)) {
		t.Errorf("first/last = %v / %v", slots[0], slots[13])
	}

	appt, err := svc.Book(ctx, pat, doc, ten)
	if err != nil {
		t.Fatal(err)
	}
	slots, _ = svc.FreeSlots(ctx, doc, mon)
	if len(slots) != 13 || slices.ContainsFunc(slots, ten.Equal) {
		t.Errorf("after booking: %d slots, still contains 10:00 = %v", len(slots), slices.ContainsFunc(slots, ten.Equal))
	}

	if err := svc.Cancel(ctx, pat, appt.AppointmentID, reason); err != nil {
		t.Fatal(err)
	}
	slots, _ = svc.FreeSlots(ctx, doc, mon)
	if len(slots) != 14 || !slices.ContainsFunc(slots, ten.Equal) {
		t.Errorf("after cancel: %d slots, want 14 including 10:00", len(slots))
	}
}

func TestFreeSlotsEmptyAndErrors(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	doc := testutil.NewDoctor(t, pool, "doc")
	addShift(t, pool, doc)
	mon := monday()

	_, err := pool.Exec(ctx,
		"INSERT INTO doctor_time_off (doctor_id, start_date, end_date) VALUES ($1, $2, $2)",
		doc, mon.AddDate(0, 0, 7))
	if err != nil {
		t.Fatal(err)
	}

	for name, day := range map[string]time.Time{
		"day without a shift": mon.AddDate(0, 0, 1),
		"saturday":            mon.AddDate(0, 0, 5),
		"day on leave":        mon.AddDate(0, 0, 7),
		"past day":            mon.AddDate(0, 0, -14),
	} {
		slots, err := svc.FreeSlots(ctx, doc, day)
		if err != nil || len(slots) != 0 {
			t.Errorf("%s: (%d slots, %v), want none", name, len(slots), err)
		}
	}

	if _, err := svc.FreeSlots(ctx, 9999, mon); !errors.Is(err, scheduling.ErrDoctorNotFound) {
		t.Errorf("unknown doctor: err = %v, want ErrDoctorNotFound", err)
	}
}

// ---- concurrency ----

// Many patients race for one slot: exactly one wins, the rest see ErrSlotTaken.
func TestConcurrentBookingSameSlot(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	doc := testutil.NewDoctor(t, pool, "doc")
	addShift(t, pool, doc)
	slot := monday().Add(10 * time.Hour)

	const n = 20
	patients := make([]auth.Principal, n)
	for i := range patients {
		patients[i] = patient(t, pool, fmt.Sprintf("pat%d", i))
	}

	errs := make([]error, n)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-release // start everyone at once
			_, errs[i] = svc.Book(ctx, patients[i], doc, slot)
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
	if c := bookedCount(t, pool, doc); c != 1 {
		t.Errorf("booked rows = %d, want 1", c)
	}
}

// One patient tries to book the same time with many doctors at once: exactly one succeeds.
func TestConcurrentBookingSamePatient(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	pat := patient(t, pool, "pat")
	slot := monday().Add(10 * time.Hour)

	const n = 10
	doctors := make([]int32, n)
	for i := range doctors {
		doctors[i] = testutil.NewDoctor(t, pool, fmt.Sprintf("doc%d", i))
		addShift(t, pool, doctors[i])
	}

	errs := make([]error, n)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-release
			_, errs[i] = svc.Book(ctx, pat, doctors[i], slot)
		}(i)
	}
	close(release)
	wg.Wait()

	var won, busy int
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, booking.ErrPatientBusy):
			busy++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if won != 1 || busy != n-1 {
		t.Fatalf("won = %d, busy = %d, want 1 and %d", won, busy, n-1)
	}

	var rows int
	err := pool.QueryRow(ctx,
		"SELECT count(*) FROM appointments WHERE patient_id = $1 AND status = 'booked'", pat.UserID).Scan(&rows)
	if err != nil || rows != 1 {
		t.Errorf("booked rows = (%d, %v), want 1", rows, err)
	}
}

// Many simultaneous cancels of one appointment: exactly one succeeds, the rest see ErrAlreadyCancelled.
func TestConcurrentCancel(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	doc := testutil.NewDoctor(t, pool, "doc")
	addShift(t, pool, doc)
	pat := patient(t, pool, "pat")

	appt, err := svc.Book(ctx, pat, doc, monday().Add(10*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	const n = 10
	errs := make([]error, n)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-release
			errs[i] = svc.Cancel(ctx, pat, appt.AppointmentID, fmt.Sprintf("reason %d", i))
		}(i)
	}
	close(release)
	wg.Wait()

	var won, already int
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, booking.ErrAlreadyCancelled):
			already++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if won != 1 || already != n-1 {
		t.Fatalf("won = %d, already = %d, want 1 and %d", won, already, n-1)
	}
}

// A booking and a schedule change that would strand it race each other.
// Exactly one may succeed: both succeeding would leave a booking outside working hours.
func TestBookRacesScheduleChange(t *testing.T) {
	pool := testutil.NewPool(t)
	st := store.NewStore(pool)
	bk := booking.NewService(st, eat)
	sc := scheduling.NewService(st, eat)
	ctx := context.Background()

	doc := testutil.NewDoctor(t, pool, "doc")
	pat := patient(t, pool, "pat")
	slot := monday().Add(10 * time.Hour)
	open := scheduling.Shift{Weekday: 1, Start: "09:00", End: "17:00"}
	late := scheduling.Shift{Weekday: 1, Start: "11:00", End: "17:00"} // excludes 10:00

	for i := 0; i < 30; i++ {
		if _, err := pool.Exec(ctx, "DELETE FROM appointments"); err != nil {
			t.Fatal(err)
		}
		if _, err := sc.SetShift(ctx, doc, open); err != nil {
			t.Fatalf("iteration %d reset: %v", i, err)
		}

		var bookErr, setErr error
		release := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-release
			_, bookErr = bk.Book(ctx, pat, doc, slot)
		}()
		go func() {
			defer wg.Done()
			<-release
			_, setErr = sc.SetShift(ctx, doc, late)
		}()
		close(release)
		wg.Wait()

		switch {
		case bookErr == nil && errors.Is(setErr, scheduling.ErrConflictingAppointments):
		case setErr == nil && errors.Is(bookErr, booking.ErrSlotUnavailable):
		default:
			t.Fatalf("iteration %d: book err = %v, set err = %v; want exactly one to succeed", i, bookErr, setErr)
		}
	}
}
