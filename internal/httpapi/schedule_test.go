package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"clinic/internal/apperr"
	"clinic/internal/scheduling"
)

type fakeSchedule struct {
	set   func(doctorID int32, in scheduling.Shift) (scheduling.Shift, error)
	clear func(doctorID int32, weekday int) error
	get   func(doctorID int32) ([]scheduling.Shift, error)
}

func (f *fakeSchedule) SetShift(_ context.Context, doctorID int32, in scheduling.Shift) (scheduling.Shift, error) {
	return f.set(doctorID, in)
}
func (f *fakeSchedule) ClearShift(_ context.Context, doctorID int32, weekday int) error {
	return f.clear(doctorID, weekday)
}
func (f *fakeSchedule) GetSchedule(_ context.Context, doctorID int32) ([]scheduling.Shift, error) {
	return f.get(doctorID)
}

func okSchedule() *fakeSchedule {
	return &fakeSchedule{
		set:   func(_ int32, in scheduling.Shift) (scheduling.Shift, error) { return in, nil },
		clear: func(int32, int) error { return nil },
		get:   func(int32) ([]scheduling.Shift, error) { return nil, nil },
	}
}

func scheduleHandler(svc ScheduleService) http.Handler {
	return NewServer(authnFor(), discardLogger(), NewScheduleModule(svc)).Handler()
}

const shiftBody = `{"start":"09:00","end":"17:00","break_start":"12:00","break_end":"13:00"}`

// doctorP has id 2 and adminP has id 3 (server_test.go).
func TestScheduleAccess(t *testing.T) {
	ops := []struct {
		name, method, suffix, body string
		ok                         int
	}{
		{"read", "GET", "/schedule", "", 200},
		{"set", "PUT", "/schedule/1", shiftBody, 200},
		{"clear", "DELETE", "/schedule/1", "", 204},
	}
	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			h := scheduleHandler(okSchedule())
			check := func(who, token string, doctorID int32, want int) {
				t.Helper()
				path := fmt.Sprintf("/doctors/%d%s", doctorID, op.suffix)
				if rec := do(h, op.method, path, token, op.body); rec.Code != want {
					t.Errorf("%s: status = %d, want %d (body %s)", who, rec.Code, want, rec.Body)
				}
			}
			check("no token", "", 2, 401)
			check("patient", "pat-token", 2, 403)
			check("doctor, own schedule", "doc-token", 2, op.ok)
			check("doctor, someone else's", "doc-token", 5, 403)
			check("admin, any doctor", "adm-token", 5, op.ok)
		})
	}
}

func TestForbiddenCallersNeverReachTheService(t *testing.T) {
	boom := func(string) { t.Error("service called for a forbidden request") }
	svc := &fakeSchedule{
		set:   func(int32, scheduling.Shift) (scheduling.Shift, error) { boom("set"); return scheduling.Shift{}, nil },
		clear: func(int32, int) error { boom("clear"); return nil },
		get:   func(int32) ([]scheduling.Shift, error) { boom("get"); return nil, nil },
	}
	h := scheduleHandler(svc)

	do(h, "GET", "/doctors/5/schedule", "doc-token", "")
	do(h, "PUT", "/doctors/5/schedule/1", "doc-token", shiftBody)
	do(h, "DELETE", "/doctors/5/schedule/1", "pat-token", "")
}

func TestSetShift(t *testing.T) {
	var gotDoctor int32
	var gotShift scheduling.Shift
	svc := okSchedule()
	svc.set = func(d int32, in scheduling.Shift) (scheduling.Shift, error) {
		gotDoctor, gotShift = d, in
		return in, nil
	}
	h := scheduleHandler(svc)

	rec := do(h, "PUT", "/doctors/2/schedule/3", "doc-token", shiftBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	want := scheduling.Shift{Weekday: 3, Start: "09:00", End: "17:00", BreakStart: "12:00", BreakEnd: "13:00"}
	if gotDoctor != 2 || gotShift != want {
		t.Errorf("service got (%d, %+v), want (2, %+v)", gotDoctor, gotShift, want)
	}
	var got shiftResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got != newShiftResponse(want) {
		t.Errorf("response = (%+v, %v)", got, err)
	}

	// no break: the break fields are left out of the response
	rec = do(h, "PUT", "/doctors/2/schedule/1", "doc-token", `{"start":"08:00","end":"16:00"}`)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "break") {
		t.Errorf("no-break response = %d %s", rec.Code, rec.Body)
	}
}

func TestScheduleBadRequests(t *testing.T) {
	cases := []struct {
		name, method, path, body string
	}{
		{"weekday not a number", "PUT", "/doctors/2/schedule/abc", shiftBody},
		{"doctor id not a number", "PUT", "/doctors/abc/schedule/1", shiftBody},
		{"doctor id zero", "GET", "/doctors/0/schedule", ""},
		{"clear, weekday not a number", "DELETE", "/doctors/2/schedule/mon", ""},
		{"empty body", "PUT", "/doctors/2/schedule/1", ""},
		{"unknown field", "PUT", "/doctors/2/schedule/1", `{"start":"09:00","end":"17:00","doctor_id":9}`},
		{"wrong type", "PUT", "/doctors/2/schedule/1", `{"start":900,"end":"17:00"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc := &fakeSchedule{
				set: func(int32, scheduling.Shift) (scheduling.Shift, error) {
					t.Error("service called")
					return scheduling.Shift{}, nil
				},
				clear: func(int32, int) error { t.Error("service called"); return nil },
				get:   func(int32) ([]scheduling.Shift, error) { t.Error("service called"); return nil, nil },
			}
			if rec := do(scheduleHandler(svc), c.method, c.path, "doc-token", c.body); rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body)
			}
		})
	}
}

func TestScheduleErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"validation", &apperr.ValidationError{Problems: map[string]string{"weekday": "must be 1 (Monday) to 5 (Friday)"}},
			422, "validation_failed"},
		{"conflict", scheduling.ErrConflictingAppointments, 409, "schedule_conflict"},
		{"unknown doctor", scheduling.ErrDoctorNotFound, 404, "doctor_not_found"},
		{"no shift", fmt.Errorf("clear: %w", scheduling.ErrShiftNotFound), 404, "shift_not_found"},
		{"internal", errors.New("connection refused: password=hunter2"), 500, "internal_error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc := okSchedule()
			svc.set = func(int32, scheduling.Shift) (scheduling.Shift, error) { return scheduling.Shift{}, c.err }
			svc.clear = func(int32, int) error { return c.err }
			h := scheduleHandler(svc)

			for _, rec := range []struct {
				method, path, body string
			}{
				{"PUT", "/doctors/2/schedule/1", shiftBody},
				{"DELETE", "/doctors/2/schedule/1", ""},
			} {
				r := do(h, rec.method, rec.path, "doc-token", rec.body)
				e := decodeErr(t, r)
				if r.Code != c.status || e.Error.Code != c.code {
					t.Errorf("%s: response = %d %+v, want %d %s", rec.method, r.Code, e, c.status, c.code)
				}
				if strings.Contains(r.Body.String(), "hunter2") {
					t.Errorf("%s: internal detail leaked", rec.method)
				}
			}
		})
	}
}

func TestClearAndGetSchedule(t *testing.T) {
	var gotDoctor int32
	var gotWeekday int
	svc := okSchedule()
	svc.clear = func(d int32, wd int) error { gotDoctor, gotWeekday = d, wd; return nil }
	svc.get = func(int32) ([]scheduling.Shift, error) {
		return []scheduling.Shift{
			{Weekday: 1, Start: "09:00", End: "17:00", BreakStart: "12:00", BreakEnd: "13:00"},
			{Weekday: 2, Start: "10:00", End: "14:00"},
		}, nil
	}
	h := scheduleHandler(svc)

	if rec := do(h, "DELETE", "/doctors/2/schedule/4", "doc-token", ""); rec.Code != 204 || rec.Body.Len() != 0 ||
		gotDoctor != 2 || gotWeekday != 4 {
		t.Errorf("clear = %d %q, service got (%d, %d)", rec.Code, rec.Body, gotDoctor, gotWeekday)
	}

	rec := do(h, "GET", "/doctors/2/schedule", "doc-token", "")
	var got struct {
		DoctorID int32           `json:"doctor_id"`
		Shifts   []shiftResponse `json:"shifts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.DoctorID != 2 ||
		len(got.Shifts) != 2 || got.Shifts[0].BreakStart != "12:00" || got.Shifts[1].BreakStart != "" {
		t.Fatalf("response = %s (%v)", rec.Body, err)
	}

	if rec := do(scheduleHandler(okSchedule()), "GET", "/doctors/2/schedule", "doc-token", ""); !strings.Contains(rec.Body.String(), `"shifts":[]`) {
		t.Errorf("empty schedule = %s, want \"shifts\":[]", rec.Body)
	}
}
