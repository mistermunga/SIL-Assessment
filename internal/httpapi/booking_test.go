package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"clinic/internal/apperr"
	"clinic/internal/auth"
	"clinic/internal/booking"
	"clinic/internal/scheduling"
	"clinic/internal/store"
)

var clinicTZ = time.FixedZone("EAT", 3*60*60)

// fakeBooking stubs each service method with a function a test can replace.
type fakeBooking struct {
	book       func(p auth.Principal, doctorID int32, start time.Time) (store.Appointment, error)
	cancel     func(p auth.Principal, id int32, reason string) error
	reschedule func(p auth.Principal, id int32, start time.Time) (store.Appointment, error)
	freeSlots  func(doctorID int32, day time.Time) ([]time.Time, error)
	mine       func(p auth.Principal, upcoming bool, limit, offset int) ([]booking.PatientAppointment, error)
	agenda     func(p auth.Principal, doctorID int32, day time.Time) ([]booking.DoctorAppointment, error)
	outcome    func(p auth.Principal, id int32, outcome store.AppointmentStatus, notes string) error
	pending    func(p auth.Principal, limit int) ([]booking.DoctorAppointment, error)
}

func (f *fakeBooking) Book(_ context.Context, p auth.Principal, doctorID int32, start time.Time) (store.Appointment, error) {
	return f.book(p, doctorID, start)
}
func (f *fakeBooking) Cancel(_ context.Context, p auth.Principal, id int32, reason string) error {
	return f.cancel(p, id, reason)
}
func (f *fakeBooking) Reschedule(_ context.Context, p auth.Principal, id int32, start time.Time) (store.Appointment, error) {
	return f.reschedule(p, id, start)
}
func (f *fakeBooking) FreeSlots(_ context.Context, doctorID int32, day time.Time) ([]time.Time, error) {
	return f.freeSlots(doctorID, day)
}
func (f *fakeBooking) MyAppointments(_ context.Context, p auth.Principal, upcoming bool, limit, offset int) ([]booking.PatientAppointment, error) {
	return f.mine(p, upcoming, limit, offset)
}
func (f *fakeBooking) DoctorAgenda(_ context.Context, p auth.Principal, doctorID int32, day time.Time) ([]booking.DoctorAppointment, error) {
	return f.agenda(p, doctorID, day)
}
func (f *fakeBooking) MarkOutcome(_ context.Context, p auth.Principal, id int32, outcome store.AppointmentStatus, notes string) error {
	return f.outcome(p, id, outcome, notes)
}
func (f *fakeBooking) PendingOutcomes(_ context.Context, p auth.Principal, limit int) ([]booking.DoctorAppointment, error) {
	return f.pending(p, limit)
}

// okBooking succeeds with empty results everywhere; tests replace what they care about.
func okBooking() *fakeBooking {
	return &fakeBooking{
		book:       func(auth.Principal, int32, time.Time) (store.Appointment, error) { return store.Appointment{}, nil },
		cancel:     func(auth.Principal, int32, string) error { return nil },
		reschedule: func(auth.Principal, int32, time.Time) (store.Appointment, error) { return store.Appointment{}, nil },
		freeSlots:  func(int32, time.Time) ([]time.Time, error) { return nil, nil },
		mine:       func(auth.Principal, bool, int, int) ([]booking.PatientAppointment, error) { return nil, nil },
		agenda:     func(auth.Principal, int32, time.Time) ([]booking.DoctorAppointment, error) { return nil, nil },
		outcome:    func(auth.Principal, int32, store.AppointmentStatus, string) error { return nil },
		pending:    func(auth.Principal, int) ([]booking.DoctorAppointment, error) { return nil, nil },
	}
}

func bookingHandler(svc BookingService) http.Handler {
	return NewServer(authnFor(), discardLogger(), NewBookingModule(svc, clinicTZ)).Handler()
}

const ts = "2026-10-26T10:00:00+03:00"

func TestBookingRouteAccess(t *testing.T) {
	routes := []struct {
		name, method, path, body string
		allowed                  []string
	}{
		{"book", "POST", "/appointments", `{"doctor_id":3,"start":"` + ts + `"}`, []string{"patient"}},
		{"my appointments", "GET", "/appointments", "", []string{"patient"}},
		{"cancel", "PATCH", "/appointments/5/cancel", `{"reason":"x"}`, []string{"patient"}},
		{"reschedule", "PATCH", "/appointments/5/reschedule", `{"start":"` + ts + `"}`, []string{"patient"}},
		{"availability", "GET", "/doctors/3/availability?date=2026-10-26", "", []string{"patient", "doctor", "admin"}},
		{"agenda", "GET", "/doctors/3/appointments?date=2026-10-26", "", []string{"doctor", "admin"}},
		{"outcome", "PATCH", "/appointments/5/outcome", `{"outcome":"completed"}`, []string{"doctor", "admin"}},
		{"pending outcomes", "GET", "/doctors/me/pending-outcomes", "", []string{"doctor", "admin"}},
	}
	tokens := map[string]string{"patient": "pat-token", "doctor": "doc-token", "admin": "adm-token"}
	h := bookingHandler(okBooking())

	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			if rec := do(h, rt.method, rt.path, "", rt.body); rec.Code != http.StatusUnauthorized {
				t.Errorf("no token: status = %d, want 401", rec.Code)
			}
			for role, token := range tokens {
				rec := do(h, rt.method, rt.path, token, rt.body)
				if slices.Contains(rt.allowed, role) {
					if rec.Code < 200 || rec.Code > 299 {
						t.Errorf("%s: status = %d, want 2xx (body %s)", role, rec.Code, rec.Body)
					}
				} else if rec.Code != http.StatusForbidden {
					t.Errorf("%s: status = %d, want 403", role, rec.Code)
				}
			}
		})
	}
}

func TestBook(t *testing.T) {
	var gotP auth.Principal
	var gotDoctor int32
	var gotStart time.Time
	svc := okBooking()
	svc.book = func(p auth.Principal, d int32, s time.Time) (store.Appointment, error) {
		gotP, gotDoctor, gotStart = p, d, s
		return store.Appointment{
			AppointmentID: 9, DoctorID: d, PatientID: p.UserID,
			AppointmentStart: s.UTC(), Status: store.AppointmentStatusBooked,
		}, nil
	}

	rec := do(bookingHandler(svc), "POST", "/appointments", "pat-token", `{"doctor_id":3,"start":"`+ts+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body)
	}
	want := time.Date(2026, 10, 26, 10, 0, 0, 0, clinicTZ)
	if gotP != patientP || gotDoctor != 3 || !gotStart.Equal(want) {
		t.Errorf("service got (%+v, %d, %v), want the caller, doctor 3 and %v", gotP, gotDoctor, gotStart, want)
	}

	var got struct {
		ID        int32     `json:"id"`
		DoctorID  int32     `json:"doctor_id"`
		PatientID int32     `json:"patient_id"`
		Start     time.Time `json:"start"`
		End       time.Time `json:"end"`
		Status    string    `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != 9 || got.DoctorID != 3 || got.PatientID != patientP.UserID || got.Status != "booked" {
		t.Errorf("response = %+v", got)
	}
	if !got.Start.Equal(want) || !got.End.Equal(want.Add(30*time.Minute)) {
		t.Errorf("start/end = %v / %v, want 10:00-10:30 on the 26th", got.Start, got.End)
	}
	if _, offset := got.Start.Zone(); offset != 3*60*60 {
		t.Errorf("start is shown with offset %ds, want the clinic's +03:00", offset)
	}
}

func TestBookRejectsBadRequests(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		fields     []string
	}{
		{"nothing", `{}`, 422, []string{"doctor_id", "start"}},
		{"no start", `{"doctor_id":3}`, 422, []string{"start"}},
		{"no doctor", `{"start":"` + ts + `"}`, 422, []string{"doctor_id"}},
		{"not a timestamp", `{"doctor_id":3,"start":"tomorrow"}`, 422, []string{"start"}},
		{"no UTC offset", `{"doctor_id":3,"start":"2026-10-26T10:00:00"}`, 422, []string{"start"}},
		{"empty body", ``, 400, nil},
		{"booking for someone else", `{"doctor_id":3,"start":"` + ts + `","patient_id":9}`, 400, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			svc := okBooking()
			svc.book = func(auth.Principal, int32, time.Time) (store.Appointment, error) {
				calls++
				return store.Appointment{}, nil
			}
			rec := do(bookingHandler(svc), "POST", "/appointments", "pat-token", c.body)
			if rec.Code != c.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, c.status, rec.Body)
			}
			e := decodeErr(t, rec)
			for _, f := range c.fields {
				if _, ok := e.Error.Problems[f]; !ok {
					t.Errorf("problems = %v, want a message for %q", e.Error.Problems, f)
				}
			}
			if calls != 0 {
				t.Error("service was called for a rejected request")
			}
		})
	}
}

func TestCancel(t *testing.T) {
	var gotP auth.Principal
	var gotID int32
	var gotReason string
	svc := okBooking()
	svc.cancel = func(p auth.Principal, id int32, reason string) error {
		gotP, gotID, gotReason = p, id, reason
		return nil
	}
	h := bookingHandler(svc)

	rec := do(h, "PATCH", "/appointments/5/cancel", "pat-token", `{"reason":"schedule conflict"}`)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("response = %d %q, want an empty 204", rec.Code, rec.Body)
	}
	if gotP != patientP || gotID != 5 || gotReason != "schedule conflict" {
		t.Errorf("service got (%+v, %d, %q)", gotP, gotID, gotReason)
	}

	for _, path := range []string{
		"/appointments/abc/cancel", "/appointments/0/cancel",
		"/appointments/-1/cancel", "/appointments/99999999999/cancel",
	} {
		if rec := do(h, "PATCH", path, "pat-token", `{"reason":"x"}`); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", path, rec.Code)
		}
	}
	if rec := do(h, "PATCH", "/appointments/5/cancel", "pat-token", ``); rec.Code != http.StatusBadRequest {
		t.Errorf("empty body: status = %d, want 400", rec.Code)
	}
}

func TestReschedule(t *testing.T) {
	var gotID int32
	var gotStart time.Time
	svc := okBooking()
	svc.reschedule = func(p auth.Principal, id int32, s time.Time) (store.Appointment, error) {
		gotID, gotStart = id, s
		return store.Appointment{
			AppointmentID: id, DoctorID: 3, PatientID: p.UserID,
			AppointmentStart: s.UTC(), Status: store.AppointmentStatusBooked,
		}, nil
	}
	h := bookingHandler(svc)

	rec := do(h, "PATCH", "/appointments/5/reschedule", "pat-token", `{"start":"`+ts+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	if want := time.Date(2026, 10, 26, 10, 0, 0, 0, clinicTZ); gotID != 5 || !gotStart.Equal(want) {
		t.Errorf("service got (%d, %v)", gotID, gotStart)
	}
	var got appointmentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.ID != 5 || got.Status != "booked" {
		t.Errorf("response = (%+v, %v)", got, err)
	}

	for name, body := range map[string]string{"missing start": `{}`, "bad start": `{"start":"soon"}`} {
		if rec := do(h, "PATCH", "/appointments/5/reschedule", "pat-token", body); rec.Code != 422 {
			t.Errorf("%s: status = %d, want 422", name, rec.Code)
		}
	}
}

func TestAvailability(t *testing.T) {
	var gotDoctor int32
	var gotDay time.Time
	svc := okBooking()
	svc.freeSlots = func(d int32, day time.Time) ([]time.Time, error) {
		gotDoctor, gotDay = d, day
		return []time.Time{
			time.Date(2026, 10, 26, 9, 0, 0, 0, clinicTZ),
			time.Date(2026, 10, 26, 9, 30, 0, 0, clinicTZ),
		}, nil
	}
	h := bookingHandler(svc)

	rec := do(h, "GET", "/doctors/3/availability?date=2026-10-26", "pat-token", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	if want := time.Date(2026, 10, 26, 0, 0, 0, 0, clinicTZ); gotDoctor != 3 || !gotDay.Equal(want) {
		t.Errorf("service got (%d, %v), want doctor 3 and clinic-zone midnight", gotDoctor, gotDay)
	}
	var got struct {
		DoctorID int32  `json:"doctor_id"`
		Date     string `json:"date"`
		Slots    []struct {
			Start time.Time `json:"start"`
			End   time.Time `json:"end"`
		} `json:"slots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.DoctorID != 3 || got.Date != "2026-10-26" || len(got.Slots) != 2 {
		t.Fatalf("response = %+v", got)
	}
	if !got.Slots[0].End.Equal(got.Slots[1].Start) {
		t.Errorf("slots are not back to back: %+v", got.Slots)
	}

	// no slots encodes as an empty list, not null
	empty := do(bookingHandler(okBooking()), "GET", "/doctors/3/availability?date=2026-10-26", "pat-token", "")
	if !strings.Contains(empty.Body.String(), `"slots":[]`) {
		t.Errorf("empty availability = %s, want \"slots\":[]", empty.Body)
	}

	for name, c := range map[string]struct {
		path   string
		status int
	}{
		"no date":       {"/doctors/3/availability", 422},
		"day first":     {"/doctors/3/availability?date=26-10-2026", 422},
		"not a day":     {"/doctors/3/availability?date=2026-13-45", 422},
		"bad doctor id": {"/doctors/abc/availability?date=2026-10-26", 400},
	} {
		if rec := do(h, "GET", c.path, "pat-token", ""); rec.Code != c.status {
			t.Errorf("%s: status = %d, want %d", name, rec.Code, c.status)
		}
	}
}

func TestMyAppointments(t *testing.T) {
	var gotUpcoming bool
	var gotLimit, gotOffset int
	svc := okBooking()
	svc.mine = func(_ auth.Principal, upcoming bool, limit, offset int) ([]booking.PatientAppointment, error) {
		gotUpcoming, gotLimit, gotOffset = upcoming, limit, offset
		return nil, nil
	}
	h := bookingHandler(svc)

	for _, c := range []struct {
		query         string
		upcoming      bool
		limit, offset int
	}{
		{"", false, 0, 0},
		{"?upcoming=true", true, 0, 0},
		{"?upcoming=true&limit=10&offset=5", true, 10, 5},
	} {
		if rec := do(h, "GET", "/appointments"+c.query, "pat-token", ""); rec.Code != 200 {
			t.Fatalf("%q: status = %d", c.query, rec.Code)
		}
		if gotUpcoming != c.upcoming || gotLimit != c.limit || gotOffset != c.offset {
			t.Errorf("%q: service got (%v, %d, %d), want (%v, %d, %d)", c.query,
				gotUpcoming, gotLimit, gotOffset, c.upcoming, c.limit, c.offset)
		}
	}

	for _, q := range []string{"?upcoming=maybe", "?limit=abc", "?offset=1.5", "?limit=99999999999"} {
		if rec := do(h, "GET", "/appointments"+q, "pat-token", ""); rec.Code != 422 {
			t.Errorf("%q: status = %d, want 422", q, rec.Code)
		}
	}
}

func TestMyAppointmentsResponse(t *testing.T) {
	svc := okBooking()
	svc.mine = func(auth.Principal, bool, int, int) ([]booking.PatientAppointment, error) {
		return []booking.PatientAppointment{{
			ID: 4, DoctorID: 3, DoctorName: "Greg House",
			Start:  time.Date(2026, 10, 26, 7, 0, 0, 0, time.UTC), // 10:00 clinic time
			Status: store.AppointmentStatusCancelled, Notes: "schedule conflict",
		}}, nil
	}
	rec := do(bookingHandler(svc), "GET", "/appointments", "pat-token", "")

	var got struct {
		Appointments []struct {
			ID         int32     `json:"id"`
			DoctorID   int32     `json:"doctor_id"`
			DoctorName string    `json:"doctor_name"`
			Start      time.Time `json:"start"`
			End        time.Time `json:"end"`
			Status     string    `json:"status"`
			Notes      string    `json:"notes"`
		} `json:"appointments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Appointments) != 1 {
		t.Fatalf("response = (%s, %v)", rec.Body, err)
	}
	a := got.Appointments[0]
	if a.ID != 4 || a.DoctorID != 3 || a.DoctorName != "Greg House" || a.Status != "cancelled" || a.Notes != "schedule conflict" {
		t.Errorf("entry = %+v", a)
	}
	if _, offset := a.Start.Zone(); offset != 3*60*60 || a.Start.Hour() != 10 || !a.End.Equal(a.Start.Add(30*time.Minute)) {
		t.Errorf("start/end = %v / %v, want 10:00-10:30 at +03:00", a.Start, a.End)
	}

	empty := do(bookingHandler(okBooking()), "GET", "/appointments", "pat-token", "")
	if !strings.Contains(empty.Body.String(), `"appointments":[]`) {
		t.Errorf("empty list = %s, want \"appointments\":[]", empty.Body)
	}
}

func TestAgenda(t *testing.T) {
	var gotP auth.Principal
	var gotDoctor int32
	var gotDay time.Time
	svc := okBooking()
	svc.agenda = func(p auth.Principal, d int32, day time.Time) ([]booking.DoctorAppointment, error) {
		gotP, gotDoctor, gotDay = p, d, day
		return []booking.DoctorAppointment{{
			ID: 4, PatientID: 1, PatientName: "Ann Lee",
			Start: time.Date(2026, 10, 26, 10, 0, 0, 0, clinicTZ), Status: store.AppointmentStatusBooked,
		}}, nil
	}
	h := bookingHandler(svc)

	rec := do(h, "GET", "/doctors/3/appointments?date=2026-10-26", "adm-token", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	if want := time.Date(2026, 10, 26, 0, 0, 0, 0, clinicTZ); gotP != adminP || gotDoctor != 3 || !gotDay.Equal(want) {
		t.Errorf("service got (%+v, %d, %v)", gotP, gotDoctor, gotDay)
	}
	var got struct {
		DoctorID     int32  `json:"doctor_id"`
		Date         string `json:"date"`
		Appointments []struct {
			ID          int32  `json:"id"`
			PatientID   int32  `json:"patient_id"`
			PatientName string `json:"patient_name"`
			Status      string `json:"status"`
		} `json:"appointments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.DoctorID != 3 || got.Date != "2026-10-26" || len(got.Appointments) != 1 ||
		got.Appointments[0].PatientName != "Ann Lee" || got.Appointments[0].Status != "booked" {
		t.Errorf("response = %+v", got)
	}

	if rec := do(h, "GET", "/doctors/3/appointments", "doc-token", ""); rec.Code != 422 {
		t.Errorf("no date: status = %d, want 422", rec.Code)
	}
}

func TestOutcome(t *testing.T) {
	var gotP auth.Principal
	var gotID int32
	var gotOutcome store.AppointmentStatus
	var gotNotes string
	svc := okBooking()
	svc.outcome = func(p auth.Principal, id int32, o store.AppointmentStatus, notes string) error {
		gotP, gotID, gotOutcome, gotNotes = p, id, o, notes
		return nil
	}
	h := bookingHandler(svc)

	rec := do(h, "PATCH", "/appointments/5/outcome", "doc-token", `{"outcome":"no_show","notes":"did not arrive"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", rec.Code, rec.Body)
	}
	if gotP != doctorP || gotID != 5 || gotOutcome != store.AppointmentStatusNoShow || gotNotes != "did not arrive" {
		t.Errorf("service got (%+v, %d, %q, %q)", gotP, gotID, gotOutcome, gotNotes)
	}

	if rec := do(h, "PATCH", "/appointments/5/outcome", "doc-token", `{"outcome":"completed","status":"x"}`); rec.Code != 400 {
		t.Errorf("unknown field: status = %d, want 400", rec.Code)
	}
}

func TestPendingOutcomes(t *testing.T) {
	var gotLimit int
	svc := okBooking()
	svc.pending = func(_ auth.Principal, limit int) ([]booking.DoctorAppointment, error) {
		gotLimit = limit
		return nil, nil
	}
	h := bookingHandler(svc)

	if rec := do(h, "GET", "/doctors/me/pending-outcomes?limit=7", "doc-token", ""); rec.Code != 200 || gotLimit != 7 {
		t.Errorf("status = %d, limit = %d, want 200 and 7", rec.Code, gotLimit)
	}
	if rec := do(h, "GET", "/doctors/me/pending-outcomes?limit=abc", "doc-token", ""); rec.Code != 422 {
		t.Errorf("bad limit: status = %d, want 422", rec.Code)
	}
}

func TestBookingErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"unavailable", fmt.Errorf("%w: outside working hours", booking.ErrSlotUnavailable), 422, "slot_unavailable"},
		{"slot taken", booking.ErrSlotTaken, 409, "slot_taken"},
		{"patient busy", booking.ErrPatientBusy, 409, "patient_busy"},
		{"appointment not found", booking.ErrNotFound, 404, "appointment_not_found"},
		{"already cancelled", booking.ErrAlreadyCancelled, 409, "already_cancelled"},
		{"not cancellable", fmt.Errorf("%w: already started", booking.ErrNotCancellable), 409, "not_cancellable"},
		{"not reschedulable", fmt.Errorf("%w: already started", booking.ErrNotReschedulable), 409, "not_reschedulable"},
		{"not markable", fmt.Errorf("%w: not started", booking.ErrNotMarkable), 409, "not_markable"},
		{"doctor not found", scheduling.ErrDoctorNotFound, 404, "doctor_not_found"},
		{"forbidden", apperr.ErrForbidden, 403, "forbidden"},
		{"internal", errors.New("connection refused: password=hunter2"), 500, "internal_error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc := okBooking()
			svc.book = func(auth.Principal, int32, time.Time) (store.Appointment, error) {
				return store.Appointment{}, c.err
			}
			rec := do(bookingHandler(svc), "POST", "/appointments", "pat-token", `{"doctor_id":3,"start":"`+ts+`"}`)
			e := decodeErr(t, rec)
			if rec.Code != c.status || e.Error.Code != c.code {
				t.Fatalf("response = %d %+v, want %d %s", rec.Code, e, c.status, c.code)
			}
			if strings.Contains(rec.Body.String(), "hunter2") {
				t.Error("internal detail leaked to the client")
			}
		})
	}
}

func TestAllModulesRegisterWithoutConflict(t *testing.T) {
	NewServer(authnFor(), discardLogger(),
		NewHealthModule(fakePinger{}),
		NewAccountsModule(&fakeRegistrar{}, &fakeIssuer{}),
		NewAuthModule(&fakeLogin{}),
		NewBookingModule(okBooking(), clinicTZ),
		NewDoctorsModule(&fakeDoctors{}),
	).Handler()
}
