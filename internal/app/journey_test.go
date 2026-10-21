package app_test

import (
	"strings"
	"testing"
)

// One story from an empty database to a finished appointment, with real logins.
func TestJourney(t *testing.T) {
	e := newEnv(t)
	monday := nextMonday()

	// the service is up, and health needs no token
	must(t, e.do("GET", "/healthz", "", nil), 200)

	// the seeded administrator signs in; nobody is bookable yet
	adminToken := e.login("administrator", adminPassword)
	r := e.do("GET", "/doctors", adminToken, nil)
	must(t, r, 200)
	if !strings.Contains(string(r.body), `"doctors":[]`) {
		t.Errorf("directory before any schedule = %s, want an empty list", r.body)
	}

	// the administrator creates a doctor, who signs in and sets a Monday schedule
	doc := e.createDoctor(adminToken, "dr_house", "Gregory", "House", false)
	doctorToken := e.login("dr_house", userPassword)
	must(t, e.do("PUT", schedulePath(doc.id, 1), doctorToken, lunchShift), 200)

	// a patient registers, is signed in at once, and finds the doctor
	alice := e.registerPatient("alice", "Alice", "Lee")
	r = e.do("GET", "/doctors", alice.token, nil)
	must(t, r, 200)
	var dir struct {
		Doctors []struct {
			ID   int32  `json:"id"`
			Name string `json:"name"`
		} `json:"doctors"`
	}
	r.decode(t, &dir)
	if len(dir.Doctors) != 1 || dir.Doctors[0].ID != doc.id || dir.Doctors[0].Name != "Gregory House" {
		t.Fatalf("directory = %s", r.body)
	}

	slots := func() []slotBody {
		r := e.do("GET", availabilityPath(doc.id, monday), alice.token, nil)
		must(t, r, 200)
		var av availabilityBody
		r.decode(t, &av)
		return av.Slots
	}
	if n := len(slots()); n != 14 {
		t.Fatalf("free slots = %d, want 14", n)
	}

	// book 10:00: the slot disappears
	r = e.do("POST", "/appointments", alice.token, map[string]any{"doctor_id": doc.id, "start": stamp(monday, 10, 0)})
	must(t, r, 201)
	var appt apptBody
	r.decode(t, &appt)
	if hasSlot(slots(), clock(monday, 10, 0)) {
		t.Error("the booked slot is still free")
	}

	// reschedule to 14:00: 10:00 returns, 14:00 goes
	r = e.do("PATCH", apptPath(appt.ID, "reschedule"), alice.token, map[string]string{"start": stamp(monday, 14, 0)})
	must(t, r, 200)
	free := slots()
	if !hasSlot(free, clock(monday, 10, 0)) || hasSlot(free, clock(monday, 14, 0)) {
		t.Error("rescheduling did not swap the free slots")
	}

	// cancel with a reason: everything is free again, and a second cancel is refused
	must(t, e.do("PATCH", apptPath(appt.ID, "cancel"), alice.token, map[string]string{"reason": "schedule conflict"}), 204)
	if n := len(slots()); n != 14 {
		t.Errorf("free slots after cancelling = %d, want 14", n)
	}
	want(t, e.do("PATCH", apptPath(appt.ID, "cancel"), alice.token, map[string]string{"reason": "again"}), 409, "already_cancelled")

	// rebook 10:00 as a new appointment
	r = e.do("POST", "/appointments", alice.token, map[string]any{"doctor_id": doc.id, "start": stamp(monday, 10, 0)})
	must(t, r, 201)
	var second apptBody
	r.decode(t, &second)
	if second.ID == appt.ID {
		t.Error("the rebooking reused the cancelled appointment")
	}

	// the patient's history, newest first, keeps the cancelled one with its reason
	r = e.do("GET", "/appointments", alice.token, nil)
	must(t, r, 200)
	var hist struct {
		Appointments []patientEntryBody `json:"appointments"`
	}
	r.decode(t, &hist)
	h := hist.Appointments
	if len(h) != 2 || h[0].ID != appt.ID || h[0].Status != "cancelled" || h[0].Notes != "schedule conflict" ||
		h[1].ID != second.ID || h[1].Status != "booked" {
		t.Fatalf("history = %s", r.body)
	}

	// the doctor's agenda shows both, in time order, with the patient's name
	r = e.do("GET", agendaPath(doc.id, monday), doctorToken, nil)
	must(t, r, 200)
	var agenda struct {
		Appointments []doctorEntryBody `json:"appointments"`
	}
	r.decode(t, &agenda)
	a := agenda.Appointments
	if len(a) != 2 || a[0].ID != second.ID || a[0].PatientName != "Alice Lee" || a[1].Status != "cancelled" {
		t.Fatalf("agenda = %s", r.body)
	}

	// an outcome cannot be recorded before the appointment starts
	outcome := map[string]string{"outcome": "completed", "notes": "all good"}
	want(t, e.do("PATCH", apptPath(second.ID, "outcome"), doctorToken, outcome), 409, "not_markable")

	// time passes: the appointment is now thirty days old
	e.exec("UPDATE appointments SET appointment_start = appointment_start - interval '30 days' WHERE appointment_id = $1", second.ID)

	r = e.do("GET", "/doctors/me/pending-outcomes", doctorToken, nil)
	must(t, r, 200)
	var pending struct {
		Appointments []doctorEntryBody `json:"appointments"`
	}
	r.decode(t, &pending)
	if len(pending.Appointments) != 1 || pending.Appointments[0].ID != second.ID {
		t.Fatalf("pending outcomes = %s", r.body)
	}

	must(t, e.do("PATCH", apptPath(second.ID, "outcome"), doctorToken, outcome), 204)
	r = e.do("GET", "/doctors/me/pending-outcomes", doctorToken, nil)
	must(t, r, 200)
	if !strings.Contains(string(r.body), `"appointments":[]`) {
		t.Errorf("pending outcomes after recording = %s, want empty", r.body)
	}

	// the patient sees the outcome and can no longer cancel
	r = e.do("GET", "/appointments", alice.token, nil)
	must(t, r, 200)
	r.decode(t, &hist)
	var done *patientEntryBody
	for i := range hist.Appointments {
		if hist.Appointments[i].ID == second.ID {
			done = &hist.Appointments[i]
		}
	}
	if done == nil || done.Status != "completed" || done.Notes != "all good" {
		t.Errorf("the completed appointment = %+v", done)
	}
	want(t, e.do("PATCH", apptPath(second.ID, "cancel"), alice.token, map[string]string{"reason": "too late"}), 409, "not_cancellable")
}
