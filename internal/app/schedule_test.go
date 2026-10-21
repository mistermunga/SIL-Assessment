package app_test

import (
	"fmt"
	"strings"
	"testing"
)

func (c *clinic) shifts(p person, doctorID int32) []shiftBody {
	c.t.Helper()
	r := c.do("GET", scheduleRoot(doctorID), p.token, nil)
	must(c.t, r, 200)
	var got struct {
		DoctorID int32       `json:"doctor_id"`
		Shifts   []shiftBody `json:"shifts"`
	}
	r.decode(c.t, &got)
	if got.DoctorID != doctorID {
		c.t.Errorf("doctor_id = %d, want %d", got.DoctorID, doctorID)
	}
	return got.Shifts
}

func isLunchMonday(s shiftBody) bool {
	return s.Weekday == 1 && s.Start == "09:00" && s.End == "17:00" &&
		s.BreakStart == "12:00" && s.BreakEnd == "13:00"
}

func TestScheduleAuthorization(t *testing.T) {
	c := newClinic(t)

	type op struct {
		method, path string
		body         any
		ok           int
	}
	opsFor := func(doctorID int32) []op {
		return []op{
			{"GET", scheduleRoot(doctorID), nil, 200},
			{"PUT", schedulePath(doctorID, 3), lunchShift, 200},
			{"DELETE", schedulePath(doctorID, 3), nil, 204},
		}
	}

	// refused without a token, as a patient, or as a different doctor
	for _, o := range opsFor(c.house.id) {
		want(t, c.do(o.method, o.path, "", o.body), 401, "unauthorized")
		want(t, c.do(o.method, o.path, c.alice.token, o.body), 403, "forbidden")
		want(t, c.do(o.method, o.path, c.adams.token, o.body), 403, "forbidden")
	}
	if n := c.count("SELECT count(*) FROM doctor_schedules WHERE doctor_id = $1", c.house.id); n != 1 {
		t.Errorf("a refused request changed the schedule: %d rows, want 1", n)
	}

	// a refused caller learns nothing about validation
	want(t, c.do("PUT", schedulePath(c.house.id, 1), c.adams.token, `{"start":`), 403, "forbidden")

	// a doctor manages their own schedule, an administrator anyone's
	for _, o := range opsFor(c.house.id) {
		want(t, c.do(o.method, o.path, c.house.token, o.body), o.ok, "")
	}
	for _, o := range opsFor(c.adams.id) {
		want(t, c.do(o.method, o.path, c.admin.token, o.body), o.ok, "")
	}
}

func TestScheduleLifecycle(t *testing.T) {
	c := newClinic(t)
	doc := c.house

	// a shift without a break leaves the break fields out of the response
	r := c.do("PUT", schedulePath(doc.id, 3), doc.token, map[string]string{"start": "10:00", "end": "14:00"})
	must(t, r, 200)
	var wed shiftBody
	r.decode(t, &wed)
	if wed.Weekday != 3 || wed.Start != "10:00" || wed.End != "14:00" {
		t.Errorf("saved shift = %+v", wed)
	}
	if strings.Contains(string(r.body), "break") {
		t.Errorf("the response mentions a break: %s", r.body)
	}

	// a shift with a break, starting on the half hour
	fri := map[string]string{"start": "08:30", "end": "12:30", "break_start": "10:00", "break_end": "10:30"}
	r = c.do("PUT", schedulePath(doc.id, 5), doc.token, fri)
	must(t, r, 200)
	var friday shiftBody
	r.decode(t, &friday)
	if friday.Weekday != 5 || friday.Start != "08:30" || friday.BreakStart != "10:00" || friday.BreakEnd != "10:30" {
		t.Errorf("saved shift = %+v", friday)
	}

	shifts := c.shifts(doc, doc.id)
	if len(shifts) != 3 || shifts[0].Weekday != 1 || shifts[1].Weekday != 3 || shifts[2].Weekday != 5 {
		t.Fatalf("shifts = %+v, want Monday, Wednesday and Friday in that order", shifts)
	}
	if !isLunchMonday(shifts[0]) || shifts[1].BreakStart != "" || shifts[2].Start != "08:30" {
		t.Errorf("shifts = %+v", shifts)
	}

	// putting a day again replaces it
	must(t, c.do("PUT", schedulePath(doc.id, 1), doc.token, map[string]string{"start": "08:00", "end": "16:00"}), 200)
	shifts = c.shifts(doc, doc.id)
	if len(shifts) != 3 || shifts[0].Start != "08:00" || shifts[0].End != "16:00" || shifts[0].BreakStart != "" {
		t.Fatalf("after replacing Monday: %+v", shifts)
	}
	if n := len(c.slots(doc.id)); n != 16 { // 08:00-16:00 with no break
		t.Errorf("free slots after replacing Monday = %d, want 16", n)
	}

	// clearing a day removes it; clearing a day without a shift is a 404
	must(t, c.do("DELETE", schedulePath(doc.id, 3), doc.token, nil), 204)
	if n := len(c.shifts(doc, doc.id)); n != 2 {
		t.Errorf("shifts after clearing Wednesday = %d, want 2", n)
	}
	want(t, c.do("DELETE", schedulePath(doc.id, 3), doc.token, nil), 404, "shift_not_found")
	want(t, c.do("DELETE", schedulePath(doc.id, 2), doc.token, nil), 404, "shift_not_found")

	// a cleared Monday has no slots and takes no bookings; other doctors are unaffected
	must(t, c.do("DELETE", schedulePath(doc.id, 1), doc.token, nil), 204)
	if n := len(c.slots(doc.id)); n != 0 {
		t.Errorf("free slots on a cleared day = %d, want 0", n)
	}
	want(t, c.book(c.alice, doc.id, 10, 0), 422, "slot_unavailable")
	if n := len(c.slots(c.adams.id)); n != 14 {
		t.Errorf("another doctor's slots = %d, want 14", n)
	}
}

func TestScheduleValidation(t *testing.T) {
	c := newClinic(t)
	path := schedulePath(c.house.id, 1)

	cases := []struct {
		name   string
		body   map[string]string
		fields []string
	}{
		{"empty object", map[string]string{}, []string{"start", "end"}},
		{"off the half hour", map[string]string{"start": "09:15", "end": "17:00"}, []string{"start"}},
		{"not a time", map[string]string{"start": "9am", "end": "17:00"}, []string{"start"}},
		{"24:00", map[string]string{"start": "09:00", "end": "24:00"}, []string{"end"}},
		{"end before start", map[string]string{"start": "17:00", "end": "09:00"}, []string{"end"}},
		{"end equals start", map[string]string{"start": "09:00", "end": "09:00"}, []string{"end"}},
		{"half a break", map[string]string{"start": "09:00", "end": "17:00", "break_start": "12:00"}, []string{"break"}},
		{"break after the shift", map[string]string{"start": "09:00", "end": "17:00", "break_start": "16:30", "break_end": "17:30"}, []string{"break"}},
		{"break before the shift", map[string]string{"start": "09:00", "end": "17:00", "break_start": "08:00", "break_end": "09:30"}, []string{"break"}},
		{"break over an hour", map[string]string{"start": "09:00", "end": "17:00", "break_start": "12:00", "break_end": "13:30"}, []string{"break"}},
		{"break backwards", map[string]string{"start": "09:00", "end": "17:00", "break_start": "13:00", "break_end": "12:00"}, []string{"break_end"}},
		{"break off the grid", map[string]string{"start": "09:00", "end": "17:00", "break_start": "12:10", "break_end": "13:00"}, []string{"break_start"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := c.do("PUT", path, c.house.token, tc.body)
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

	// weekdays outside Monday to Friday, for both setting and clearing
	for _, wd := range []int{-1, 0, 6, 7, 9} {
		put := c.do("PUT", schedulePath(c.house.id, wd), c.house.token, lunchShift)
		if want(t, put, 422, "validation_failed") && put.errEnv().Error.Problems["weekday"] == "" {
			t.Errorf("PUT weekday %d: no problem reported for weekday", wd)
		}
		del := c.do("DELETE", schedulePath(c.house.id, wd), c.house.token, nil)
		if want(t, del, 422, "validation_failed") && del.errEnv().Error.Problems["weekday"] == "" {
			t.Errorf("DELETE weekday %d: no problem reported for weekday", wd)
		}
	}

	// nothing above changed the schedule
	if shifts := c.shifts(c.house, c.house.id); len(shifts) != 1 || !isLunchMonday(shifts[0]) {
		t.Errorf("schedule after rejected requests = %+v, want only the original Monday", shifts)
	}
}

func TestScheduleBadRequests(t *testing.T) {
	c := newClinic(t)
	id := fmt.Sprint(c.house.id)
	at := func(doctor, weekday string) string { return "/doctors/" + doctor + "/schedule/" + weekday }

	cases := []struct {
		name, method, path string
		body               any
	}{
		{"weekday not a number", "PUT", at(id, "abc"), lunchShift},
		{"doctor id not a number", "PUT", at("abc", "1"), lunchShift},
		{"doctor id zero", "GET", "/doctors/0/schedule", nil},
		{"doctor id negative", "GET", "/doctors/-3/schedule", nil},
		{"clear: weekday not a number", "DELETE", at(id, "mon"), nil},
		{"empty body", "PUT", at(id, "1"), ""},
		{"malformed body", "PUT", at(id, "1"), `{"start":`},
		{"unknown field", "PUT", at(id, "1"), `{"start":"09:00","end":"17:00","doctor_id":9}`},
		{"wrong type", "PUT", at(id, "1"), `{"start":900,"end":"17:00"}`},
		{"two objects", "PUT", at(id, "1"), `{"start":"09:00","end":"17:00"} {}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want(t, c.do(tc.method, tc.path, c.house.token, tc.body), 400, "bad_request")
		})
	}
	if shifts := c.shifts(c.house, c.house.id); len(shifts) != 1 || !isLunchMonday(shifts[0]) {
		t.Errorf("schedule after rejected requests = %+v", shifts)
	}
}

func TestScheduleUnknownDoctors(t *testing.T) {
	c := newClinic(t)

	// as an administrator, who may address any doctor
	for name, id := range map[string]int32{"unknown id": 999999, "a patient": c.alice.id} {
		t.Run(name, func(t *testing.T) {
			want(t, c.do("PUT", schedulePath(id, 1), c.admin.token, lunchShift), 404, "doctor_not_found")
			want(t, c.do("DELETE", schedulePath(id, 1), c.admin.token, nil), 404, "doctor_not_found")
		})
	}

	// reading an unknown doctor's schedule gives an empty list
	r := c.do("GET", scheduleRoot(999999), c.admin.token, nil)
	must(t, r, 200)
	if !strings.Contains(string(r.body), `"shifts":[]`) {
		t.Errorf("response = %s, want an empty list of shifts", r.body)
	}
}

// A schedule change is refused while a future booked appointment would no longer fit.
func TestScheduleChangesRespectBookings(t *testing.T) {
	c := newClinic(t)
	appt := c.mustBook(c.alice, c.house.id, 10, 0)
	monday := schedulePath(c.house.id, 1)

	conflicts := map[string]map[string]string{
		"starts after the booking": {"start": "11:00", "end": "17:00"},
		"ends before the booking":  {"start": "09:00", "end": "10:00"},
		"break over the booking":   {"start": "09:00", "end": "17:00", "break_start": "10:00", "break_end": "11:00"},
	}
	for name, body := range conflicts {
		t.Run(name, func(t *testing.T) {
			want(t, c.do("PUT", monday, c.house.token, body), 409, "schedule_conflict")
		})
	}
	want(t, c.do("DELETE", monday, c.house.token, nil), 409, "schedule_conflict")
	if shifts := c.shifts(c.house, c.house.id); len(shifts) != 1 || !isLunchMonday(shifts[0]) {
		t.Fatalf("a refused change altered the schedule: %+v", shifts)
	}

	// other weekdays and changes that still hold the booking are fine
	must(t, c.do("PUT", schedulePath(c.house.id, 2), c.house.token, lunchShift), 200)
	must(t, c.do("PUT", monday, c.house.token, map[string]string{"start": "10:00", "end": "12:00"}), 200)

	// once the appointment is cancelled, nothing holds the day
	must(t, c.cancel(c.alice, appt.ID, "changed my mind"), 204)
	must(t, c.do("PUT", monday, c.house.token, map[string]string{"start": "11:00", "end": "17:00"}), 200)
	must(t, c.do("DELETE", monday, c.house.token, nil), 204)
}
