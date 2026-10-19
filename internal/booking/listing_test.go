package booking_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"clinic/internal/apperr"
	"clinic/internal/auth"
	"clinic/internal/booking"
	"clinic/internal/scheduling"
	"clinic/internal/store"
	"clinic/internal/testutil"
)

func patientIDs(l []booking.PatientAppointment) []int32 {
	out := make([]int32, len(l))
	for i, a := range l {
		out[i] = a.ID
	}
	return out
}

func TestMyAppointments(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	doc := testutil.NewDoctor(t, pool, "doc")
	addShift(t, pool, doc)
	pat, other := patient(t, pool, "pat"), patient(t, pool, "other")
	mon := monday()

	if _, err := pool.Exec(ctx,
		"UPDATE users SET firstname = 'Greg', lastname = 'House' WHERE user_id = $1", doc); err != nil {
		t.Fatal(err)
	}

	book := func(p auth.Principal, d time.Duration) int32 {
		a, err := svc.Book(ctx, p, doc, mon.Add(d))
		if err != nil {
			t.Fatal(err)
		}
		return a.AppointmentID
	}
	a1, a2, a3 := book(pat, 10*time.Hour), book(pat, 11*time.Hour), book(pat, 14*time.Hour)
	book(other, 15*time.Hour) // must never appear in pat's list
	if err := svc.Cancel(ctx, pat, a3, reason); err != nil {
		t.Fatal(err)
	}

	var past int32
	err := pool.QueryRow(ctx, `INSERT INTO appointments (doctor_id, patient_id, appointment_start)
		VALUES ($1, $2, $3) RETURNING appointment_id`,
		doc, pat.UserID, time.Now().UTC().Truncate(time.Hour).AddDate(0, 0, -1)).Scan(&past)
	if err != nil {
		t.Fatal(err)
	}

	// everything, newest first
	all, err := svc.MyAppointments(ctx, pat, false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int32{a3, a2, a1, past}; !slices.Equal(patientIDs(all), want) {
		t.Fatalf("all = %v, want %v", patientIDs(all), want)
	}
	if all[0].Status != store.AppointmentStatusCancelled || all[0].Notes != reason {
		t.Errorf("cancelled entry = %+v", all[0])
	}
	if all[1].DoctorName != "Greg House" || all[1].DoctorID != doc {
		t.Errorf("doctor on entry = %q (%d)", all[1].DoctorName, all[1].DoctorID)
	}

	// upcoming: booked and in the future only, soonest first
	upcoming, err := svc.MyAppointments(ctx, pat, true, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int32{a1, a2}; !slices.Equal(patientIDs(upcoming), want) {
		t.Errorf("upcoming = %v, want %v", patientIDs(upcoming), want)
	}

	// paging
	page, err := svc.MyAppointments(ctx, pat, false, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int32{a2, a1}; !slices.Equal(patientIDs(page), want) {
		t.Errorf("page = %v, want %v", patientIDs(page), want)
	}

	asDoctor := auth.Principal{UserID: doc, Username: "doc", Role: auth.RoleDoctor}
	if _, err := svc.MyAppointments(ctx, asDoctor, false, 0, 0); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("doctor: err = %v, want ErrForbidden", err)
	}
}

func TestMyAppointmentsEmpty(t *testing.T) {
	svc, pool := setup(t)
	list, err := svc.MyAppointments(context.Background(), patient(t, pool, "pat"), false, 0, 0)
	if err != nil || list == nil || len(list) != 0 {
		t.Fatalf("(%v, %v), want an empty non-nil slice", list, err)
	}
}

func TestDoctorAgenda(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	doc, otherDoc := testutil.NewDoctor(t, pool, "doc"), testutil.NewDoctor(t, pool, "doc2")
	addShift(t, pool, doc)
	p1, p2 := patient(t, pool, "p1"), patient(t, pool, "p2")
	mon := monday()

	if _, err := pool.Exec(ctx,
		"UPDATE users SET firstname = 'Ann', lastname = 'Lee' WHERE user_id = $1", p1.UserID); err != nil {
		t.Fatal(err)
	}

	book := func(p auth.Principal, d time.Duration) int32 {
		a, err := svc.Book(ctx, p, doc, mon.Add(d))
		if err != nil {
			t.Fatal(err)
		}
		return a.AppointmentID
	}
	first := book(p1, 10*time.Hour)
	book(p2, 11*time.Hour)
	cancelled := book(p1, 14*time.Hour)
	if err := svc.Cancel(ctx, p1, cancelled, reason); err != nil {
		t.Fatal(err)
	}

	asDoc := auth.Principal{UserID: doc, Username: "doc", Role: auth.RoleDoctor}
	got, err := svc.DoctorAgenda(ctx, asDoc, doc, mon)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("agenda = %d entries, want 3", len(got))
	}
	if got[0].ID != first || got[0].PatientName != "Ann Lee" || got[0].PatientID != p1.UserID {
		t.Errorf("first entry = %+v", got[0])
	}
	if got[2].ID != cancelled || got[2].Status != store.AppointmentStatusCancelled {
		t.Errorf("last entry = %+v, want the cancelled one in start order", got[2])
	}

	empty, err := svc.DoctorAgenda(ctx, asDoc, doc, mon.AddDate(0, 0, 1))
	if err != nil || empty == nil || len(empty) != 0 {
		t.Errorf("next day = (%v, %v), want an empty non-nil slice", empty, err)
	}

	// only that doctor or an administrator may read it
	asOther := auth.Principal{UserID: otherDoc, Username: "doc2", Role: auth.RoleDoctor}
	if _, err := svc.DoctorAgenda(ctx, asOther, doc, mon); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("other doctor: err = %v, want ErrForbidden", err)
	}
	if _, err := svc.DoctorAgenda(ctx, p1, doc, mon); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("patient: err = %v, want ErrForbidden", err)
	}
	admin := auth.Principal{UserID: otherDoc, Username: "boss", Role: auth.RoleAdmin}
	if list, err := svc.DoctorAgenda(ctx, admin, doc, mon); err != nil || len(list) != 3 {
		t.Errorf("admin = (%d entries, %v), want 3", len(list), err)
	}
	if _, err := svc.DoctorAgenda(ctx, admin, 9999, mon); !errors.Is(err, scheduling.ErrDoctorNotFound) {
		t.Errorf("unknown doctor: err = %v, want ErrDoctorNotFound", err)
	}
}
