package app_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// ---- helpers ----

func (e *env) text(sql string, args ...any) string {
	e.t.Helper()
	var s string
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		e.t.Fatalf("query %q: %v", sql, err)
	}
	return s
}

func (e *env) startOf(id int32) time.Time {
	e.t.Helper()
	var s time.Time
	err := e.pool.QueryRow(context.Background(),
		"SELECT appointment_start FROM appointments WHERE appointment_id = $1", id).Scan(&s)
	if err != nil {
		e.t.Fatalf("read the start of appointment %d: %v", id, err)
	}
	return s
}

func (c *clinic) status(id int32) string {
	c.t.Helper()
	return c.text("SELECT status::text FROM appointments WHERE appointment_id = $1", id)
}

func (c *clinic) notes(id int32) string {
	c.t.Helper()
	return c.text("SELECT COALESCE(notes, '') FROM appointments WHERE appointment_id = $1", id)
}

func (c *clinic) reschedule(p person, id int32, start string) resp {
	return c.do("PATCH", apptPath(id, "reschedule"), p.token, map[string]string{"start": start})
}

func (c *clinic) outcome(p person, id int32, body any) resp {
	return c.do("PATCH", apptPath(id, "outcome"), p.token, body)
}

func findEntry(es []patientEntryBody, id int32) *patientEntryBody {
	for i := range es {
		if es[i].ID == id {
			return &es[i]
		}
	}
	return nil
}

// badSlots lists start times the clinic must refuse, and gives Dr House
// a day of leave on the Monday after c.monday.
func (c *clinic) badSlots() map[string]string {
	c.exec("INSERT INTO doctor_time_off (doctor_id, start_date, end_date) VALUES ($1, $2, $2)",
		c.house.id, c.monday.AddDate(0, 0, 7))
	return map[string]string{
		"before the shift":      stamp(c.monday, 8, 0),
		"start of the break":    stamp(c.monday, 12, 0),
		"inside the break":      stamp(c.monday, 12, 30),
		"at shift end":          stamp(c.monday, 17, 0),
		"off the half hour":     stamp(c.monday, 9, 15),
		"with seconds":          c.monday.Add(9*time.Hour + 30*time.Second).Format(time.RFC3339),
		"a day without a shift": stamp(c.monday.AddDate(0, 0, 1), 10, 0),
		"a weekend":             stamp(c.monday.AddDate(0, 0, 5), 10, 0),
		"on leave":              stamp(c.monday.AddDate(0, 0, 7), 10, 0),
		"in the past":           stamp(c.monday.AddDate(0, 0, -14), 10, 0),
	}
}

// ---- booking ----

func TestBook(t *testing.T) {
	c := newClinic(t)

	r := c.book(c.alice, c.house.id, 10, 0)
	must(t, r, 201)
	var a apptBody
	r.decode(t, &a)
	start := clock(c.monday, 10, 0)
	if a.ID == 0 || a.DoctorID != c.house.id || a.PatientID != c.alice.id || a.Status != "booked" || a.Notes != "" {
		t.Errorf("appointment = %+v", a)
	}
	if !a.Start.Equal(start) || !a.End.Equal(start.Add(30*time.Minute)) {
		t.Errorf("start/end = %v / %v, want 10:00-10:30", a.Start, a.End)
	}
	if _, offset := a.Start.Zone(); offset != 3*60*60 {
		t.Errorf("start is shown at offset %ds, want the clinic's +03:00", offset)
	}
	if n := c.count("SELECT count(*) FROM appointments WHERE appointment_id = $1 AND status = 'booked'", a.ID); n != 1 {
		t.Errorf("booked rows = %d, want 1", n)
	}

	// any UTC offset is accepted: 11:00 clinic time, written in UTC
	r = c.bookAt(c.bob, c.house.id, clock(c.monday, 11, 0).UTC().Format(time.RFC3339))
	must(t, r, 201)
	var b apptBody
	r.decode(t, &b)
	if !b.Start.Equal(clock(c.monday, 11, 0)) {
		t.Errorf("start = %v, want 11:00 clinic time", b.Start)
	}
	if _, offset := b.Start.Zone(); offset != 3*60*60 {
		t.Errorf("start is shown at offset %ds, want the clinic's +03:00", offset)
	}

	// the slots on either side of the break are bookable
	must(t, c.book(c.alice, c.house.id, 11, 30), 201)
	must(t, c.book(c.alice, c.house.id, 13, 0), 201)
}

func TestBookRejectsUnavailableSlots(t *testing.T) {
	c := newClinic(t)
	for name, start := range c.badSlots() {
		t.Run(name, func(t *testing.T) {
			want(t, c.bookAt(c.alice, c.house.id, start), 422, "slot_unavailable")
		})
	}
	if n := c.count("SELECT count(*) FROM appointments"); n != 0 {
		t.Errorf("appointments = %d, want 0", n)
	}
}

func TestBookUnknownOrInactiveDoctors(t *testing.T) {
	c := newClinic(t)

	want(t, c.book(c.alice, 999999, 10, 0), 404, "doctor_not_found")
	want(t, c.book(c.alice, c.bob.id, 10, 0), 404, "doctor_not_found")   // a patient is not a doctor
	want(t, c.book(c.alice, c.admin.id, 10, 0), 422, "slot_unavailable") // the administrator has no schedule

	c.exec("UPDATE users SET deleted_at = now() WHERE user_id = $1", c.adams.id)
	want(t, c.book(c.alice, c.adams.id, 10, 0), 404, "doctor_not_found")
}

func TestBookRejectsBadRequests(t *testing.T) {
	c := newClinic(t)
	ten := stamp(c.monday, 10, 0)

	invalid := []struct {
		name   string
		body   any
		fields []string
	}{
		{"empty object", map[string]any{}, []string{"doctor_id", "start"}},
		{"no start", map[string]any{"doctor_id": c.house.id}, []string{"start"}},
		{"no doctor", map[string]any{"start": ten}, []string{"doctor_id"}},
		{"doctor zero", map[string]any{"doctor_id": 0, "start": ten}, []string{"doctor_id"}},
		{"doctor negative", map[string]any{"doctor_id": -1, "start": ten}, []string{"doctor_id"}},
		{"not a timestamp", map[string]any{"doctor_id": c.house.id, "start": "tomorrow"}, []string{"start"}},
		{"no UTC offset", map[string]any{"doctor_id": c.house.id, "start": dateStr(c.monday) + "T10:00:00"}, []string{"start"}},
		{"date only", map[string]any{"doctor_id": c.house.id, "start": dateStr(c.monday)}, []string{"start"}},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			r := c.do("POST", "/appointments", c.alice.token, tc.body)
			if !want(t, r, 422, "validation_failed") {
				return
			}
			for _, f := range tc.fields {
				if r.errEnv().Error.Problems[f] == "" {
					t.Errorf("no problem reported for %q (problems %v)", f, r.errEnv().Error.Problems)
				}
			}
		})
	}

	malformed := map[string]string{
		"empty body":               "",
		"malformed":                `{"doctor_id":`,
		"doctor id as a string":    `{"doctor_id":"3","start":"` + ten + `"}`,
		"start as a number":        `{"doctor_id":3,"start":1000}`,
		"booking for someone else": `{"doctor_id":3,"start":"` + ten + `","patient_id":9}`,
	}
	for name, body := range malformed {
		t.Run(name, func(t *testing.T) {
			want(t, c.do("POST", "/appointments", c.alice.token, body), 400, "bad_request")
		})
	}
	if n := c.count("SELECT count(*) FROM appointments"); n != 0 {
		t.Errorf("appointments = %d, want 0", n)
	}
}

func TestBookConflicts(t *testing.T) {
	c := newClinic(t)
	first := c.mustBook(c.alice, c.house.id, 10, 0)

	// another patient, same doctor, same slot
	want(t, c.book(c.bob, c.house.id, 10, 0), 409, "slot_taken")

	// the same patient, same time, another doctor
	want(t, c.book(c.alice, c.adams.id, 10, 0), 409, "patient_busy")

	// re-booking the slot already held breaks both rules; either report is correct
	r := c.book(c.alice, c.house.id, 10, 0)
	if code := r.errEnv().Error.Code; r.status != 409 || (code != "slot_taken" && code != "patient_busy") {
		t.Errorf("re-booking own slot = %d %s, want a 409 slot_taken or patient_busy", r.status, r.body)
	}

	// adjacent slots are fine
	must(t, c.book(c.alice, c.adams.id, 10, 30), 201)
	must(t, c.book(c.bob, c.house.id, 10, 30), 201)

	// cancelling frees both the doctor's slot and the patient's time
	must(t, c.cancel(c.alice, first.ID, "changed my mind"), 204)
	must(t, c.book(c.bob, c.adams.id, 10, 0), 201)
	must(t, c.book(c.alice, c.house.id, 10, 0), 201)
}

// ---- availability ----

func TestAvailability(t *testing.T) {
	c := newClinic(t)

	r := c.do("GET", availabilityPath(c.house.id, c.monday), c.alice.token, nil)
	must(t, r, 200)
	var av availabilityBody
	r.decode(t, &av)
	if av.DoctorID != c.house.id || av.Date != dateStr(c.monday) {
		t.Errorf("response = %+v", av)
	}
	if len(av.Slots) != 14 { // 16 half-hours in the shift, minus 2 in the break
		t.Fatalf("slots = %d, want 14", len(av.Slots))
	}
	for i, s := range av.Slots {
		if !s.End.Equal(s.Start.Add(30 * time.Minute)) {
			t.Errorf("slot %d lasts %v, want 30 minutes", i, s.End.Sub(s.Start))
		}
		if _, offset := s.Start.Zone(); offset != 3*60*60 {
			t.Errorf("slot %d is shown at offset %ds, want +03:00", i, offset)
		}
		if i > 0 && s.Start.Before(av.Slots[i-1].End) {
			t.Errorf("slot %d overlaps or precedes the one before it", i)
		}
	}
	if !av.Slots[0].Start.Equal(clock(c.monday, 9, 0)) || !av.Slots[13].Start.Equal(clock(c.monday, 16, 30)) {
		t.Errorf("first/last = %v / %v, want 09:00 and 16:30", av.Slots[0].Start, av.Slots[13].Start)
	}
	for _, in := range [][2]int{{12, 0}, {12, 30}} {
		if hasSlot(av.Slots, clock(c.monday, in[0], in[1])) {
			t.Errorf("a slot at %02d:%02d falls inside the break", in[0], in[1])
		}
	}
	for _, edge := range [][2]int{{11, 30}, {13, 0}} {
		if !hasSlot(av.Slots, clock(c.monday, edge[0], edge[1])) {
			t.Errorf("no slot at %02d:%02d, next to the break", edge[0], edge[1])
		}
	}

	// every signed-in role may read availability
	for _, tok := range []string{c.bob.token, c.house.token, c.adams.token, c.admin.token} {
		want(t, c.do("GET", availabilityPath(c.house.id, c.monday), tok, nil), 200, "")
	}
}

func TestAvailabilityTracksBookings(t *testing.T) {
	c := newClinic(t)
	ten := clock(c.monday, 10, 0)

	a := c.mustBook(c.alice, c.house.id, 10, 0)
	slots := c.slots(c.house.id)
	if len(slots) != 13 || hasSlot(slots, ten) {
		t.Errorf("after booking: %d slots, 10:00 still free = %v", len(slots), hasSlot(slots, ten))
	}
	if n := len(c.slots(c.adams.id)); n != 14 {
		t.Errorf("another doctor's slots = %d, want 14", n)
	}

	must(t, c.cancel(c.alice, a.ID, "changed my mind"), 204)
	slots = c.slots(c.house.id)
	if len(slots) != 14 || !hasSlot(slots, ten) {
		t.Errorf("after cancelling: %d slots, 10:00 free = %v", len(slots), hasSlot(slots, ten))
	}
}

func TestAvailabilityOnDaysWithNoSlots(t *testing.T) {
	c := newClinic(t)
	c.exec("INSERT INTO doctor_time_off (doctor_id, start_date, end_date) VALUES ($1, $2, $2)",
		c.house.id, c.monday.AddDate(0, 0, 7))

	empty := func(name string, doctorID int32, day time.Time) {
		t.Helper()
		r := c.do("GET", availabilityPath(doctorID, day), c.alice.token, nil)
		must(t, r, 200)
		if !strings.Contains(string(r.body), `"slots":[]`) {
			t.Errorf("%s: response = %s, want an empty list of slots", name, r.body)
		}
	}
	empty("a day without a shift", c.house.id, c.monday.AddDate(0, 0, 1))
	empty("a weekend", c.house.id, c.monday.AddDate(0, 0, 5))
	empty("a day in the past", c.house.id, c.monday.AddDate(0, 0, -14))
	empty("a day of leave", c.house.id, c.monday.AddDate(0, 0, 7))
	empty("a doctor without a schedule", c.admin.id, c.monday)
}

func TestAvailabilityErrors(t *testing.T) {
	c := newClinic(t)
	date := dateStr(c.monday)

	want(t, c.do("GET", availabilityPath(999999, c.monday), c.alice.token, nil), 404, "doctor_not_found")
	want(t, c.do("GET", availabilityPath(c.bob.id, c.monday), c.alice.token, nil), 404, "doctor_not_found")
	c.exec("UPDATE users SET deleted_at = now() WHERE user_id = $1", c.adams.id)
	want(t, c.do("GET", availabilityPath(c.adams.id, c.monday), c.alice.token, nil), 404, "doctor_not_found")

	base := fmt.Sprintf("/doctors/%d/availability", c.house.id)
	for name, path := range map[string]string{
		"no date":     base,
		"empty date":  base + "?date=",
		"day first":   base + "?date=26-10-2026",
		"not a day":   base + "?date=2026-13-45",
		"no padding":  base + "?date=2026-1-5",
		"with a time": base + "?date=" + date + "T10:00:00",
	} {
		r := c.do("GET", path, c.alice.token, nil)
		if want(t, r, 422, "validation_failed") && r.errEnv().Error.Problems["date"] == "" {
			t.Errorf("%s: no problem reported for date", name)
		}
	}

	for _, id := range []string{"abc", "0", "-1", "99999999999"} {
		want(t, c.do("GET", "/doctors/"+id+"/availability?date="+date, c.alice.token, nil), 400, "bad_request")
	}
}

// ---- cancelling ----

func TestCancel(t *testing.T) {
	c := newClinic(t)
	a := c.mustBook(c.alice, c.house.id, 10, 0)

	// someone else's appointment and an unknown one look the same
	want(t, c.cancel(c.bob, a.ID, "x"), 404, "appointment_not_found")
	want(t, c.cancel(c.alice, 999999, "x"), 404, "appointment_not_found")

	// a reason is required
	for name, reason := range map[string]string{"empty": "", "whitespace": "  \t ", "too long": strings.Repeat("x", 501)} {
		r := c.cancel(c.alice, a.ID, reason)
		if want(t, r, 422, "validation_failed") && r.errEnv().Error.Problems["reason"] == "" {
			t.Errorf("%s: no problem reported for reason", name)
		}
	}
	want(t, c.do("PATCH", apptPath(a.ID, "cancel"), c.alice.token, map[string]string{}), 422, "validation_failed")
	for name, body := range map[string]string{
		"empty body":    "",
		"malformed":     `{"reason":`,
		"unknown field": `{"reason":"x","status":"completed"}`,
		"wrong type":    `{"reason":5}`,
	} {
		t.Run(name, func(t *testing.T) {
			want(t, c.do("PATCH", apptPath(a.ID, "cancel"), c.alice.token, body), 400, "bad_request")
		})
	}
	for _, id := range []string{"abc", "0", "-1", "99999999999"} {
		want(t, c.do("PATCH", "/appointments/"+id+"/cancel", c.alice.token, `{"reason":"x"}`), 400, "bad_request")
	}
	if st := c.status(a.ID); st != "booked" {
		t.Fatalf("a refused cancel changed the status to %s", st)
	}

	// success: 204, reason trimmed and stored, slot free again
	must(t, c.cancel(c.alice, a.ID, "  schedule conflict  "), 204)
	if got := c.notes(a.ID); got != "schedule conflict" {
		t.Errorf("notes = %q, want the trimmed reason", got)
	}
	if !hasSlot(c.slots(c.house.id), clock(c.monday, 10, 0)) {
		t.Error("the cancelled slot is not free again")
	}
	entry := findEntry(c.mine(c.alice, ""), a.ID)
	if entry == nil || entry.Status != "cancelled" || entry.Notes != "schedule conflict" {
		t.Errorf("the patient's list shows %+v", entry)
	}

	// a second cancel is refused and leaves the original reason
	want(t, c.cancel(c.alice, a.ID, "another reason"), 409, "already_cancelled")
	if got := c.notes(a.ID); got != "schedule conflict" {
		t.Errorf("notes after a second cancel = %q, want the original reason", got)
	}
}

func TestCancelRefusesStartedAppointments(t *testing.T) {
	c := newClinic(t)
	started := c.pastAppointment(c.house.id, c.alice.id, 1, "booked")
	completed := c.pastAppointment(c.house.id, c.alice.id, 2, "completed")
	noShow := c.pastAppointment(c.house.id, c.alice.id, 3, "no_show")

	for name, id := range map[string]int32{"started": started, "completed": completed, "no-show": noShow} {
		t.Run(name, func(t *testing.T) {
			want(t, c.cancel(c.alice, id, "too late"), 409, "not_cancellable")
		})
	}
	if st := c.status(started); st != "booked" {
		t.Errorf("status = %s, want booked", st)
	}
}

// ---- rescheduling ----

func TestReschedule(t *testing.T) {
	c := newClinic(t)
	a := c.mustBook(c.alice, c.house.id, 10, 0)

	r := c.reschedule(c.alice, a.ID, stamp(c.monday, 14, 0))
	must(t, r, 200)
	var moved apptBody
	r.decode(t, &moved)
	start := clock(c.monday, 14, 0)
	if moved.ID != a.ID || moved.DoctorID != c.house.id || moved.PatientID != c.alice.id || moved.Status != "booked" {
		t.Errorf("appointment = %+v", moved)
	}
	if !moved.Start.Equal(start) || !moved.End.Equal(start.Add(30*time.Minute)) {
		t.Errorf("start/end = %v / %v, want 14:00-14:30", moved.Start, moved.End)
	}

	free := c.slots(c.house.id)
	if !hasSlot(free, clock(c.monday, 10, 0)) || hasSlot(free, start) {
		t.Error("rescheduling did not swap the free slots")
	}
	if n := c.count("SELECT count(*) FROM appointments"); n != 1 {
		t.Errorf("appointments = %d, want 1 (the row is moved, not copied)", n)
	}
	if n := c.count("SELECT count(*) FROM appointments WHERE patient_id = $1 AND status = 'booked'", c.alice.id); n != 1 {
		t.Errorf("booked appointments = %d, want 1", n)
	}

	// the slot just freed can be booked at once
	must(t, c.book(c.bob, c.house.id, 10, 0), 201)
}

func TestRescheduleRejectsUnavailableSlots(t *testing.T) {
	c := newClinic(t)
	a := c.mustBook(c.alice, c.house.id, 10, 0)
	original := clock(c.monday, 10, 0)

	for name, start := range c.badSlots() {
		t.Run(name, func(t *testing.T) {
			want(t, c.reschedule(c.alice, a.ID, start), 422, "slot_unavailable")
			if got := c.startOf(a.ID); !got.Equal(original) {
				t.Errorf("the appointment moved to %v despite the error", got)
			}
		})
	}
}

func TestRescheduleConflicts(t *testing.T) {
	c := newClinic(t)
	mine := c.mustBook(c.alice, c.house.id, 14, 0)
	c.mustBook(c.bob, c.house.id, 10, 0)   // the slot someone else holds
	c.mustBook(c.alice, c.adams.id, 11, 0) // Alice's own booking with another doctor
	original := clock(c.monday, 14, 0)

	want(t, c.reschedule(c.alice, mine.ID, stamp(c.monday, 10, 0)), 409, "slot_taken")
	want(t, c.reschedule(c.alice, mine.ID, stamp(c.monday, 11, 0)), 409, "patient_busy")
	if got := c.startOf(mine.ID); !got.Equal(original) {
		t.Errorf("the appointment moved to %v despite the errors", got)
	}

	// moving to the time it already has is a request error
	r := c.reschedule(c.alice, mine.ID, stamp(c.monday, 14, 0))
	if want(t, r, 422, "validation_failed") && r.errEnv().Error.Problems["start"] == "" {
		t.Error("no problem reported for start")
	}
}

func TestRescheduleStateErrors(t *testing.T) {
	c := newClinic(t)
	a := c.mustBook(c.alice, c.house.id, 10, 0)
	target := stamp(c.monday, 14, 0)

	want(t, c.reschedule(c.bob, a.ID, target), 404, "appointment_not_found")
	want(t, c.reschedule(c.alice, 999999, target), 404, "appointment_not_found")

	started := c.pastAppointment(c.house.id, c.alice.id, 1, "booked")
	completed := c.pastAppointment(c.house.id, c.alice.id, 2, "completed")
	noShow := c.pastAppointment(c.house.id, c.alice.id, 3, "no_show")
	for name, id := range map[string]int32{"started": started, "completed": completed, "no-show": noShow} {
		t.Run(name, func(t *testing.T) {
			want(t, c.reschedule(c.alice, id, target), 409, "not_reschedulable")
		})
	}

	// a cancelled appointment cannot be moved
	must(t, c.cancel(c.alice, a.ID, "changed my mind"), 204)
	want(t, c.reschedule(c.alice, a.ID, target), 409, "already_cancelled")
	if got := c.startOf(a.ID); !got.Equal(clock(c.monday, 10, 0)) {
		t.Errorf("a cancelled appointment moved to %v", got)
	}
	if st := c.status(a.ID); st != "cancelled" {
		t.Errorf("status = %s, want cancelled", st)
	}
}

func TestRescheduleBadRequests(t *testing.T) {
	c := newClinic(t)
	a := c.mustBook(c.alice, c.house.id, 10, 0)
	path := apptPath(a.ID, "reschedule")

	for name, body := range map[string]map[string]string{
		"no start":        {},
		"not a timestamp": {"start": "soon"},
		"no UTC offset":   {"start": dateStr(c.monday) + "T14:00:00"},
	} {
		r := c.do("PATCH", path, c.alice.token, body)
		if want(t, r, 422, "validation_failed") && r.errEnv().Error.Problems["start"] == "" {
			t.Errorf("%s: no problem reported for start", name)
		}
	}
	for name, body := range map[string]string{
		"empty body":        "",
		"malformed":         `{"start":`,
		"switching doctors": `{"start":"` + stamp(c.monday, 14, 0) + `","doctor_id":9}`,
		"start as a number": `{"start":1400}`,
	} {
		t.Run(name, func(t *testing.T) {
			want(t, c.do("PATCH", path, c.alice.token, body), 400, "bad_request")
		})
	}
	for _, id := range []string{"abc", "0", "-1"} {
		want(t, c.do("PATCH", "/appointments/"+id+"/reschedule", c.alice.token, `{"start":"`+stamp(c.monday, 14, 0)+`"}`), 400, "bad_request")
	}
	if got := c.startOf(a.ID); !got.Equal(clock(c.monday, 10, 0)) {
		t.Errorf("a refused request moved the appointment to %v", got)
	}
}

// ---- listings ----

func TestMyAppointments(t *testing.T) {
	c := newClinic(t)

	a1 := c.mustBook(c.alice, c.house.id, 10, 0)
	a2 := c.mustBook(c.alice, c.adams.id, 11, 0)
	a3 := c.mustBook(c.alice, c.house.id, 14, 0)
	b1 := c.mustBook(c.bob, c.house.id, 15, 0) // never in Alice's list
	must(t, c.cancel(c.alice, a3.ID, "schedule conflict"), 204)
	past := c.pastAppointment(c.house.id, c.alice.id, 1, "completed")

	// everything, newest first
	all := c.mine(c.alice, "")
	if got := ids(all); !slices.Equal(got, []int32{a3.ID, a2.ID, a1.ID, past}) {
		t.Fatalf("all = %v, want %v", got, []int32{a3.ID, a2.ID, a1.ID, past})
	}
	if all[0].Status != "cancelled" || all[0].Notes != "schedule conflict" {
		t.Errorf("cancelled entry = %+v", all[0])
	}
	if all[1].DoctorID != c.adams.id || all[1].DoctorName != "Allison Adams" {
		t.Errorf("doctor on the second entry = %d %q", all[1].DoctorID, all[1].DoctorName)
	}
	if all[2].DoctorID != c.house.id || all[2].DoctorName != "Gregory House" {
		t.Errorf("doctor on the third entry = %d %q", all[2].DoctorID, all[2].DoctorName)
	}
	for _, e := range all {
		if !e.End.Equal(e.Start.Add(30 * time.Minute)) {
			t.Errorf("appointment %d lasts %v, want 30 minutes", e.ID, e.End.Sub(e.Start))
		}
		if _, offset := e.Start.Zone(); offset != 3*60*60 {
			t.Errorf("appointment %d is shown at offset %ds, want +03:00", e.ID, offset)
		}
	}

	// upcoming: booked and in the future, soonest first
	if got := ids(c.mine(c.alice, "?upcoming=true")); !slices.Equal(got, []int32{a1.ID, a2.ID}) {
		t.Errorf("upcoming = %v, want %v", got, []int32{a1.ID, a2.ID})
	}
	if got := ids(c.mine(c.alice, "?upcoming=true&limit=1&offset=1")); !slices.Equal(got, []int32{a2.ID}) {
		t.Errorf("upcoming page = %v, want %v", got, []int32{a2.ID})
	}

	// paging
	if got := ids(c.mine(c.alice, "?limit=2&offset=1")); !slices.Equal(got, []int32{a2.ID, a1.ID}) {
		t.Errorf("page = %v, want %v", got, []int32{a2.ID, a1.ID})
	}
	if got := c.mine(c.alice, "?offset=50"); len(got) != 0 {
		t.Errorf("a page past the end has %d entries, want 0", len(got))
	}

	// each patient sees only their own
	if got := ids(c.mine(c.bob, "")); !slices.Equal(got, []int32{b1.ID}) {
		t.Errorf("Bob's list = %v, want %v", got, []int32{b1.ID})
	}

	// no appointments at all gives an empty list, not null
	newcomer := c.addPatient("newcomer", "New", "Comer")
	r := c.do("GET", "/appointments", newcomer.token, nil)
	must(t, r, 200)
	if !strings.Contains(string(r.body), `"appointments":[]`) {
		t.Errorf("empty list = %s, want \"appointments\":[]", r.body)
	}

	// bad query values
	for q, field := range map[string]string{
		"?upcoming=maybe":    "upcoming",
		"?limit=abc":         "limit",
		"?offset=1.5":        "offset",
		"?limit=99999999999": "limit",
	} {
		r := c.do("GET", "/appointments"+q, c.alice.token, nil)
		if want(t, r, 422, "validation_failed") && r.errEnv().Error.Problems[field] == "" {
			t.Errorf("%s: no problem reported for %s", q, field)
		}
	}
}

func TestMyAppointmentsPageSizes(t *testing.T) {
	c := newClinic(t)
	c.exec(`INSERT INTO appointments (doctor_id, patient_id, appointment_start, status)
		SELECT $1::int, $2::int, date_trunc('hour', now()) - make_interval(mins => 30 * g), 'completed'::appointment_status
		FROM generate_series(1, 105) AS g`, c.house.id, c.alice.id)

	for q, wantLen := range map[string]int{
		"":                     50,  // the default page
		"?limit=1000":          100, // capped
		"?limit=-5":            50,  // not positive: the default
		"?limit=10&offset=100": 5,
	} {
		if got := len(c.mine(c.alice, q)); got != wantLen {
			t.Errorf("GET /appointments%s returned %d entries, want %d", q, got, wantLen)
		}
	}
}

func TestDoctorAgenda(t *testing.T) {
	c := newClinic(t)
	a10 := c.mustBook(c.alice, c.house.id, 10, 0)
	a11 := c.mustBook(c.bob, c.house.id, 11, 0)
	a14 := c.mustBook(c.alice, c.house.id, 14, 0)
	c.mustBook(c.alice, c.adams.id, 9, 0) // another doctor's: never on House's agenda
	must(t, c.cancel(c.alice, a14.ID, "schedule conflict"), 204)

	path := agendaPath(c.house.id, c.monday)
	r := c.do("GET", path, c.house.token, nil)
	agenda := c.doctorList(r)
	if got := doctorIDs(agenda); !slices.Equal(got, []int32{a10.ID, a11.ID, a14.ID}) {
		t.Fatalf("agenda = %v, want %v in time order", got, []int32{a10.ID, a11.ID, a14.ID})
	}
	if agenda[0].PatientName != "Alice Lee" || agenda[0].PatientID != c.alice.id || agenda[0].Status != "booked" {
		t.Errorf("first entry = %+v", agenda[0])
	}
	if agenda[1].PatientName != "Bob Ray" {
		t.Errorf("second entry = %+v", agenda[1])
	}
	if agenda[2].Status != "cancelled" || agenda[2].Notes != "schedule conflict" {
		t.Errorf("cancelled entry = %+v", agenda[2])
	}
	for _, e := range agenda {
		if !e.End.Equal(e.Start.Add(30 * time.Minute)) {
			t.Errorf("appointment %d lasts %v, want 30 minutes", e.ID, e.End.Sub(e.Start))
		}
	}
	var top struct {
		DoctorID int32  `json:"doctor_id"`
		Date     string `json:"date"`
	}
	r.decode(t, &top)
	if top.DoctorID != c.house.id || top.Date != dateStr(c.monday) {
		t.Errorf("response header fields = %+v", top)
	}

	// administrators read any doctor's agenda; nobody else does
	if got := len(c.doctorList(c.do("GET", path, c.admin.token, nil))); got != 3 {
		t.Errorf("the administrator's view has %d entries, want 3", got)
	}
	want(t, c.do("GET", path, c.adams.token, nil), 403, "forbidden")
	want(t, c.do("GET", path, c.alice.token, nil), 403, "forbidden")
	want(t, c.do("GET", path, "", nil), 401, "unauthorized")

	// a day with nothing on it
	r = c.do("GET", agendaPath(c.house.id, c.monday.AddDate(0, 0, 1)), c.house.token, nil)
	must(t, r, 200)
	if !strings.Contains(string(r.body), `"appointments":[]`) {
		t.Errorf("empty agenda = %s", r.body)
	}

	// bad requests
	base := fmt.Sprintf("/doctors/%d/appointments", c.house.id)
	for name, p := range map[string]string{"no date": base, "bad date": base + "?date=26-10-2026"} {
		r := c.do("GET", p, c.house.token, nil)
		if want(t, r, 422, "validation_failed") && r.errEnv().Error.Problems["date"] == "" {
			t.Errorf("%s: no problem reported for date", name)
		}
	}
	want(t, c.do("GET", agendaPath(999999, c.monday), c.admin.token, nil), 404, "doctor_not_found")
	want(t, c.do("GET", agendaPath(c.bob.id, c.monday), c.admin.token, nil), 404, "doctor_not_found")
	want(t, c.do("GET", "/doctors/abc/appointments?date="+dateStr(c.monday), c.admin.token, nil), 400, "bad_request")
}

// ---- recording outcomes ----

func TestRecordOutcome(t *testing.T) {
	c := newClinic(t)
	seen := c.pastAppointment(c.house.id, c.alice.id, 1, "booked")
	missed := c.pastAppointment(c.house.id, c.bob.id, 2, "booked")

	must(t, c.outcome(c.house, seen, map[string]string{"outcome": "completed", "notes": "  follow up in two weeks "}), 204)
	must(t, c.outcome(c.house, missed, map[string]string{"outcome": "no_show"}), 204)

	if st, notes := c.status(seen), c.notes(seen); st != "completed" || notes != "follow up in two weeks" {
		t.Errorf("seen = (%s, %q), want completed with trimmed notes", st, notes)
	}
	if st, notes := c.status(missed), c.notes(missed); st != "no_show" || notes != "" {
		t.Errorf("missed = (%s, %q), want no_show with no notes", st, notes)
	}

	// the patient sees the outcome
	entry := findEntry(c.mine(c.alice, ""), seen)
	if entry == nil || entry.Status != "completed" || entry.Notes != "follow up in two weeks" {
		t.Errorf("the patient's list shows %+v", entry)
	}

	// an outcome is final
	want(t, c.outcome(c.house, seen, map[string]string{"outcome": "no_show"}), 409, "not_markable")
	want(t, c.outcome(c.house, missed, map[string]string{"outcome": "completed"}), 409, "not_markable")
}

func TestRecordOutcomeRefusals(t *testing.T) {
	c := newClinic(t)
	open := c.pastAppointment(c.house.id, c.alice.id, 1, "booked")
	cancelled := c.pastAppointment(c.house.id, c.alice.id, 2, "cancelled")
	future := c.mustBook(c.alice, c.house.id, 10, 0)
	done := map[string]string{"outcome": "completed"}

	// state
	want(t, c.outcome(c.house, future.ID, done), 409, "not_markable")
	want(t, c.outcome(c.house, cancelled, done), 409, "not_markable")

	// ownership: another doctor's appointment looks like a missing one, even for an administrator
	want(t, c.outcome(c.adams, open, done), 404, "appointment_not_found")
	want(t, c.outcome(c.admin, open, done), 404, "appointment_not_found")
	want(t, c.outcome(c.house, 999999, done), 404, "appointment_not_found")

	// callers
	want(t, c.outcome(c.alice, open, done), 403, "forbidden")
	want(t, c.outcome(person{}, open, done), 401, "unauthorized")

	// input
	for name, body := range map[string]map[string]string{
		"no outcome":           {},
		"booked as an outcome": {"outcome": "booked"},
		"cancelled":            {"outcome": "cancelled"},
		"unknown value":        {"outcome": "bogus"},
	} {
		r := c.outcome(c.house, open, body)
		if want(t, r, 422, "validation_failed") && r.errEnv().Error.Problems["outcome"] == "" {
			t.Errorf("%s: no problem reported for outcome", name)
		}
	}
	r := c.outcome(c.house, open, map[string]string{"outcome": "completed", "notes": strings.Repeat("x", 501)})
	if want(t, r, 422, "validation_failed") && r.errEnv().Error.Problems["notes"] == "" {
		t.Error("no problem reported for notes")
	}
	for name, body := range map[string]string{
		"empty body":    "",
		"malformed":     `{"outcome":`,
		"unknown field": `{"outcome":"completed","status":"x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			want(t, c.outcome(c.house, open, body), 400, "bad_request")
		})
	}
	for _, id := range []string{"abc", "0", "-1"} {
		want(t, c.do("PATCH", "/appointments/"+id+"/outcome", c.house.token, `{"outcome":"completed"}`), 400, "bad_request")
	}

	if st := c.status(open); st != "booked" {
		t.Errorf("a refused request changed the status to %s", st)
	}
}

func TestPendingOutcomes(t *testing.T) {
	c := newClinic(t)
	older := c.pastAppointment(c.house.id, c.alice.id, 3, "booked")
	newer := c.pastAppointment(c.house.id, c.alice.id, 1, "booked")
	c.pastAppointment(c.house.id, c.alice.id, 2, "completed")
	c.pastAppointment(c.house.id, c.alice.id, 4, "cancelled")
	c.pastAppointment(c.adams.id, c.bob.id, 1, "booked") // another doctor's
	c.mustBook(c.bob, c.house.id, 10, 0)                 // in the future

	r := c.do("GET", "/doctors/me/pending-outcomes", c.house.token, nil)
	if got := doctorIDs(c.doctorList(r)); !slices.Equal(got, []int32{older, newer}) {
		t.Fatalf("pending = %v, want %v (oldest first)", got, []int32{older, newer})
	}
	if got := doctorIDs(c.doctorList(c.do("GET", "/doctors/me/pending-outcomes?limit=1", c.house.token, nil))); !slices.Equal(got, []int32{older}) {
		t.Errorf("limit 1 = %v, want only the oldest", got)
	}

	// recording an outcome removes it from the list
	must(t, c.outcome(c.house, older, map[string]string{"outcome": "completed"}), 204)
	if got := doctorIDs(c.doctorList(c.do("GET", "/doctors/me/pending-outcomes", c.house.token, nil))); !slices.Equal(got, []int32{newer}) {
		t.Errorf("after marking: %v, want only %d", got, newer)
	}

	// another doctor sees only their own
	if got := len(c.doctorList(c.do("GET", "/doctors/me/pending-outcomes", c.adams.token, nil))); got != 1 {
		t.Errorf("Dr Adams has %d pending, want 1", got)
	}

	want(t, c.do("GET", "/doctors/me/pending-outcomes?limit=abc", c.house.token, nil), 422, "validation_failed")
	want(t, c.do("GET", "/doctors/me/pending-outcomes", c.alice.token, nil), 403, "forbidden")
}
