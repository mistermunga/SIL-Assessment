package app_test

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// parallel runs fn(0..n-1) at the same moment.
func parallel(n int, fn func(i int)) {
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-release
			fn(i)
		}(i)
	}
	close(release)
	wg.Wait()
}

func race(n int, fn func(i int) resp) []resp {
	out := make([]resp, n)
	parallel(n, func(i int) { out[i] = fn(i) })
	return out
}

// tally counts responses by outcome: "201", or "409 slot_taken" for errors.
func tally(rs []resp) map[string]int {
	m := map[string]int{}
	for _, r := range rs {
		key := strconv.Itoa(r.status)
		if r.status >= 400 {
			key += " " + r.errEnv().Error.Code
		}
		m[key]++
	}
	return m
}

// racerSlot gives ten distinct bookable times on the Monday shift (09:00-17:00, break 12:00-13:00).
func racerSlot(i int) (hour, minute int) {
	mins := 9*60 + i*30
	if mins >= 12*60 {
		mins += 60
	}
	return mins / 60, mins % 60
}

func TestConcurrentBookingSameSlot(t *testing.T) {
	c := newClinic(t)
	const n = 20
	patients := make([]person, n)
	for i := range patients {
		patients[i] = c.addPatient(fmt.Sprintf("racer%d", i), "Racer", fmt.Sprintf("N%d", i))
	}
	start := stamp(c.monday, 10, 0)

	results := race(n, func(i int) resp {
		return c.try("POST", "/appointments", patients[i].token, map[string]any{"doctor_id": c.house.id, "start": start})
	})
	if got := tally(results); got["201"] != 1 || got["409 slot_taken"] != n-1 || len(got) != 2 {
		t.Fatalf("outcomes = %v, want one 201 and %d 409 slot_taken", got, n-1)
	}
	if rows := c.count("SELECT count(*) FROM appointments WHERE doctor_id = $1 AND status = 'booked'", c.house.id); rows != 1 {
		t.Errorf("booked rows = %d, want 1", rows)
	}

	// through the API, exactly one patient has the appointment
	total := 0
	for _, p := range patients {
		total += len(c.mine(p, ""))
	}
	if total != 1 {
		t.Errorf("patients see %d appointments in total, want 1", total)
	}
}

func TestConcurrentBookingSamePatient(t *testing.T) {
	c := newClinic(t)
	const n = 10
	doctors := make([]person, n)
	for i := range doctors {
		doctors[i] = c.addDoctor(fmt.Sprintf("racedoc%d", i), "Race", fmt.Sprintf("Doc%d", i))
		c.withLunchShift(doctors[i].id)
	}
	start := stamp(c.monday, 10, 0)

	results := race(n, func(i int) resp {
		return c.try("POST", "/appointments", c.alice.token, map[string]any{"doctor_id": doctors[i].id, "start": start})
	})
	if got := tally(results); got["201"] != 1 || got["409 patient_busy"] != n-1 || len(got) != 2 {
		t.Fatalf("outcomes = %v, want one 201 and %d 409 patient_busy", got, n-1)
	}
	if rows := c.count("SELECT count(*) FROM appointments WHERE patient_id = $1 AND status = 'booked'", c.alice.id); rows != 1 {
		t.Errorf("booked rows = %d, want 1", rows)
	}
}

func TestConcurrentCancel(t *testing.T) {
	c := newClinic(t)
	a := c.mustBook(c.alice, c.house.id, 10, 0)
	const n = 10

	results := race(n, func(i int) resp {
		return c.try("PATCH", apptPath(a.ID, "cancel"), c.alice.token, map[string]string{"reason": fmt.Sprintf("reason %d", i)})
	})
	if got := tally(results); got["204"] != 1 || got["409 already_cancelled"] != n-1 || len(got) != 2 {
		t.Fatalf("outcomes = %v, want one 204 and %d 409 already_cancelled", got, n-1)
	}

	// the stored reason is the winner's
	for i, r := range results {
		if r.status == 204 {
			if got := c.notes(a.ID); got != fmt.Sprintf("reason %d", i) {
				t.Errorf("notes = %q, want the winner's reason %q", got, fmt.Sprintf("reason %d", i))
			}
		}
	}
	if !hasSlot(c.slots(c.house.id), clock(c.monday, 10, 0)) {
		t.Error("the slot is not free after the cancellation")
	}
}

func TestConcurrentRescheduleToTheSameSlot(t *testing.T) {
	c := newClinic(t)
	const n = 10
	patients := make([]person, n)
	appts := make([]apptBody, n)
	for i := range patients {
		patients[i] = c.addPatient(fmt.Sprintf("mover%d", i), "Mover", fmt.Sprintf("N%d", i))
		h, m := racerSlot(i)
		appts[i] = c.mustBook(patients[i], c.house.id, h, m)
	}
	target := stamp(c.monday, 16, 0)

	results := race(n, func(i int) resp {
		return c.try("PATCH", apptPath(appts[i].ID, "reschedule"), patients[i].token, map[string]string{"start": target})
	})
	if got := tally(results); got["200"] != 1 || got["409 slot_taken"] != n-1 || len(got) != 2 {
		t.Fatalf("outcomes = %v, want one 200 and %d 409 slot_taken", got, n-1)
	}

	// nobody lost an appointment, and exactly one sits at the target
	if rows := c.count("SELECT count(*) FROM appointments WHERE doctor_id = $1 AND status = 'booked'", c.house.id); rows != n {
		t.Errorf("booked rows = %d, want %d", rows, n)
	}
	atTarget := c.count("SELECT count(*) FROM appointments WHERE doctor_id = $1 AND status = 'booked' AND appointment_start = $2",
		c.house.id, clock(c.monday, 16, 0))
	if atTarget != 1 {
		t.Errorf("appointments at the target = %d, want 1", atTarget)
	}
	for i, r := range results {
		if r.status == 409 {
			h, m := racerSlot(i)
			if got := c.startOf(appts[i].ID); !got.Equal(clock(c.monday, h, m)) {
				t.Errorf("a refused move changed appointment %d to %v", appts[i].ID, got)
			}
		}
	}
}

func TestConcurrentRecordOutcome(t *testing.T) {
	c := newClinic(t)
	id := c.pastAppointment(c.house.id, c.alice.id, 1, "booked")
	const n = 10

	results := race(n, func(i int) resp {
		outcome := "completed"
		if i%2 == 1 {
			outcome = "no_show"
		}
		return c.try("PATCH", apptPath(id, "outcome"), c.house.token, map[string]string{"outcome": outcome})
	})
	if got := tally(results); got["204"] != 1 || got["409 not_markable"] != n-1 || len(got) != 2 {
		t.Fatalf("outcomes = %v, want one 204 and %d 409 not_markable", got, n-1)
	}
	if st := c.status(id); st != "completed" && st != "no_show" {
		t.Errorf("final status = %s", st)
	}
}

// A booking and a schedule change that would strand it race each other:
// exactly one wins, and no booking is ever left outside working hours.
func TestBookingRacesScheduleChange(t *testing.T) {
	c := newClinic(t)
	open := map[string]string{"start": "09:00", "end": "17:00"}
	late := map[string]string{"start": "11:00", "end": "17:00"} // excludes 10:00

	for i := 0; i < 15; i++ {
		c.exec("DELETE FROM appointments")
		must(t, c.do("PUT", schedulePath(c.house.id, 1), c.house.token, open), 200)

		results := race(2, func(k int) resp {
			if k == 0 {
				return c.try("POST", "/appointments", c.alice.token, map[string]any{"doctor_id": c.house.id, "start": stamp(c.monday, 10, 0)})
			}
			return c.try("PUT", schedulePath(c.house.id, 1), c.house.token, late)
		})
		book, put := results[0], results[1]

		booked := book.status == 201 && put.status == 409 && put.errEnv().Error.Code == "schedule_conflict"
		moved := put.status == 200 && book.status == 422 && book.errEnv().Error.Code == "slot_unavailable"
		if !booked && !moved {
			t.Fatalf("iteration %d: booking = %d %s, schedule change = %d %s; want exactly one to succeed",
				i, book.status, book.body, put.status, put.body)
		}
	}
}

// A reschedule and a cancel of the same appointment: the cancel always lands,
// the reschedule either lands first or is refused, and nothing stays booked.
func TestRescheduleRacesCancel(t *testing.T) {
	c := newClinic(t)

	for i := 0; i < 15; i++ {
		c.exec("DELETE FROM appointments")
		a := c.mustBook(c.alice, c.house.id, 10, 0)

		results := race(2, func(k int) resp {
			if k == 0 {
				return c.try("PATCH", apptPath(a.ID, "reschedule"), c.alice.token, map[string]string{"start": stamp(c.monday, 14, 0)})
			}
			return c.try("PATCH", apptPath(a.ID, "cancel"), c.alice.token, map[string]string{"reason": "race"})
		})
		move, cancel := results[0], results[1]

		if cancel.status != 204 {
			t.Fatalf("iteration %d: cancel = %d %s, want 204", i, cancel.status, cancel.body)
		}
		if move.status != 200 && !(move.status == 409 && move.errEnv().Error.Code == "already_cancelled") {
			t.Fatalf("iteration %d: reschedule = %d %s, want 200 or 409 already_cancelled", i, move.status, move.body)
		}
		if n := c.count("SELECT count(*) FROM appointments WHERE status = 'booked'"); n != 0 {
			t.Fatalf("iteration %d: %d appointments are still booked", i, n)
		}
	}
}

func TestConcurrentRegistration(t *testing.T) {
	e := newEnv(t)
	const n = 10

	// one username, many emails
	results := race(n, func(i int) resp {
		return e.try("POST", "/patients", "", map[string]string{
			"username": "twin", "email": fmt.Sprintf("twin%d@example.com", i), "password": userPassword,
		})
	})
	if got := tally(results); got["201"] != 1 || got["409 username_taken"] != n-1 || len(got) != 2 {
		t.Fatalf("same username: outcomes = %v, want one 201 and %d 409 username_taken", got, n-1)
	}

	// one email, many usernames
	results = race(n, func(i int) resp {
		return e.try("POST", "/patients", "", map[string]string{
			"username": fmt.Sprintf("sibling%d", i), "email": "shared@example.com", "password": userPassword,
		})
	})
	if got := tally(results); got["201"] != 1 || got["409 email_taken"] != n-1 || len(got) != 2 {
		t.Fatalf("same email: outcomes = %v, want one 201 and %d 409 email_taken", got, n-1)
	}

	// no half-created accounts: every user has a role
	orphans := e.count(`SELECT count(*) FROM users u
		WHERE NOT EXISTS (SELECT 1 FROM patients p WHERE p.patient_id = u.user_id)
		  AND NOT EXISTS (SELECT 1 FROM doctors d WHERE d.doctor_id = u.user_id)`)
	if orphans != 0 {
		t.Errorf("users without a role = %d, want 0", orphans)
	}
	if rows := e.count("SELECT count(*) FROM users WHERE username = 'twin' OR email = 'shared@example.com'"); rows != 2 {
		t.Errorf("accounts created = %d, want 2", rows)
	}
}

// Many patients book and cancel against a few slots at once. Whatever the interleaving,
// no request fails with a server error and the database ends up consistent.
func TestMixedBookingStorm(t *testing.T) {
	c := newClinic(t)
	const patientCount, rounds = 12, 12

	people := make([]person, patientCount)
	for i := range people {
		people[i] = c.addPatient(fmt.Sprintf("storm%d", i), "Storm", fmt.Sprintf("P%d", i))
	}
	doctors := []person{c.house, c.adams}
	times := [][2]int{{9, 0}, {9, 30}, {10, 0}, {10, 30}}

	var booked, cancelled atomic.Int64
	parallel(patientCount, func(i int) {
		p := people[i]
		for k := 0; k < rounds; k++ {
			doc := doctors[(i/2+k)%len(doctors)]
			hm := times[(i+k)%len(times)]
			r := c.try("POST", "/appointments", p.token,
				map[string]any{"doctor_id": doc.id, "start": stamp(c.monday, hm[0], hm[1])})

			switch r.status {
			case 201:
				booked.Add(1)
				if k%2 != 0 {
					continue
				}
				var a apptBody
				if err := json.Unmarshal(r.body, &a); err != nil {
					t.Errorf("cannot decode a booking: %v", err)
					continue
				}
				cr := c.try("PATCH", apptPath(a.ID, "cancel"), p.token, map[string]string{"reason": "storm"})
				if cr.status == 204 {
					cancelled.Add(1)
				} else {
					t.Errorf("cancelling own booking: status = %d (body %s)", cr.status, cr.body)
				}
			case 409:
				if code := r.errEnv().Error.Code; code != "slot_taken" && code != "patient_busy" {
					t.Errorf("booking conflict has code %q", code)
				}
			default:
				t.Errorf("booking: status = %d, want 201 or 409 (body %s)", r.status, r.body)
			}
		}
	})

	expected := int(booked.Load() - cancelled.Load())
	if got := c.count("SELECT count(*) FROM appointments WHERE status = 'booked'"); got != expected {
		t.Errorf("booked rows = %d, want %d (bookings %d minus cancellations %d)", got, expected, booked.Load(), cancelled.Load())
	}
	if got := c.count("SELECT count(*) FROM appointments WHERE status = 'cancelled'"); got != int(cancelled.Load()) {
		t.Errorf("cancelled rows = %d, want %d", got, cancelled.Load())
	}
	doubleDoctor := c.count(`SELECT count(*) FROM (SELECT 1 FROM appointments WHERE status = 'booked'
		GROUP BY doctor_id, appointment_start HAVING count(*) > 1) d`)
	doublePatient := c.count(`SELECT count(*) FROM (SELECT 1 FROM appointments WHERE status = 'booked'
		GROUP BY patient_id, appointment_start HAVING count(*) > 1) d`)
	if doubleDoctor != 0 || doublePatient != 0 {
		t.Errorf("double bookings: %d for doctors, %d for patients", doubleDoctor, doublePatient)
	}
}
