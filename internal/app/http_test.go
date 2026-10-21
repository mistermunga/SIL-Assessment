package app_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

type callers struct{ patient, doctor, admin bool }

var (
	forEveryone = callers{patient: true, doctor: true, admin: true}
	forPatients = callers{patient: true}
	forDoctors  = callers{doctor: true, admin: true} // an administrator is a doctor
	forAdmins   = callers{admin: true}
)

// Every route, called with no token, a bad token, and each role. This is the
// complete list of endpoints: a new route has to be added here.
func TestEveryRouteEnforcesItsAccessRule(t *testing.T) {
	c := newClinic(t)
	house := c.house.id
	day := dateStr(c.monday)

	routes := []struct {
		method, path string
		body         any
		open         bool
		allow        callers
	}{
		{"GET", "/healthz", nil, true, callers{}},
		{"POST", "/patients", map[string]string{}, true, callers{}},
		{"POST", "/login", map[string]string{}, true, callers{}},
		{"GET", "/doctors", nil, false, forEveryone},
		{"POST", "/doctors", map[string]string{}, false, forAdmins},
		{"GET", fmt.Sprintf("/doctors/%d/availability?date=%s", house, day), nil, false, forEveryone},
		{"GET", fmt.Sprintf("/doctors/%d/appointments?date=%s", house, day), nil, false, forDoctors},
		{"GET", "/doctors/me/pending-outcomes", nil, false, forDoctors},
		{"GET", scheduleRoot(house), nil, false, forDoctors},
		{"PUT", schedulePath(house, 1), lunchShift, false, forDoctors},
		{"DELETE", schedulePath(house, 5), nil, false, forDoctors},
		{"POST", "/appointments", map[string]string{}, false, forPatients},
		{"GET", "/appointments", nil, false, forPatients},
		{"PATCH", "/appointments/999999/cancel", map[string]string{"reason": "x"}, false, forPatients},
		{"PATCH", "/appointments/999999/reschedule", map[string]string{"start": stamp(c.monday, 10, 0)}, false, forPatients},
		{"PATCH", "/appointments/999999/outcome", map[string]string{"outcome": "completed"}, false, forDoctors},
	}

	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			if rt.open {
				anon := c.do(rt.method, rt.path, "", rt.body)
				junk := c.do(rt.method, rt.path, "not-a-token", rt.body)
				if anon.status != junk.status {
					t.Errorf("a bad token changes an open route's answer: %d vs %d", anon.status, junk.status)
				}
				if anon.status == 403 {
					t.Error("an open route answered 403")
				}
				return
			}

			want(t, c.do(rt.method, rt.path, "", rt.body), 401, "unauthorized")
			want(t, c.do(rt.method, rt.path, "not-a-token", rt.body), 401, "unauthorized")

			for _, who := range []struct {
				name, token string
				allowed     bool
			}{
				{"patient", c.alice.token, rt.allow.patient},
				{"doctor", c.house.token, rt.allow.doctor},
				{"administrator", c.admin.token, rt.allow.admin},
			} {
				r := c.do(rt.method, rt.path, who.token, rt.body)
				if who.allowed {
					if r.status == 401 || r.status == 403 {
						t.Errorf("%s was refused with %d (body %s)", who.name, r.status, r.body)
					}
				} else if r.status != 403 || r.errEnv().Error.Code != "forbidden" {
					t.Errorf("%s: status = %d (body %s), want 403 forbidden", who.name, r.status, r.body)
				}
			}
		})
	}
}

func TestHealth(t *testing.T) {
	e := newEnv(t)
	for _, token := range []string{"", "not-a-token"} {
		r := e.do("GET", "/healthz", token, nil)
		must(t, r, 200)
		var got map[string]string
		r.decode(t, &got)
		if got["status"] != "ok" {
			t.Errorf("body = %s, want status ok", r.body)
		}
	}
}

func TestUnknownRoutesAreJSON404(t *testing.T) {
	c := newClinic(t)
	paths := []string{"/", "/nope", "/doctors/", "/api/v1/doctors", "/appointments/5/cancel/extra", "/doctors/1/unknown"}
	for _, token := range []string{"", c.alice.token} {
		for _, path := range paths {
			want(t, c.do("GET", path, token, nil), 404, "route_not_found")
		}
	}
}

func TestWrongMethodsAreJSON405WithAllow(t *testing.T) {
	c := newClinic(t)
	cases := []struct {
		method, path string
		allows       []string
	}{
		{"GET", "/login", []string{"POST"}},
		{"GET", "/patients", []string{"POST"}},
		{"POST", "/healthz", []string{"GET"}},
		{"PUT", "/appointments", []string{"GET", "POST"}},
		{"DELETE", "/doctors", []string{"GET", "POST"}},
		{"DELETE", "/appointments/5/cancel", []string{"PATCH"}},
		{"POST", "/doctors/1/availability", []string{"GET"}},
		{"PATCH", "/doctors/1/schedule", []string{"GET"}},
		{"POST", "/doctors/1/schedule/1", []string{"PUT", "DELETE"}},
	}
	// the method is judged before any token check, so the answer is the same for everyone
	for _, token := range []string{"", c.alice.token} {
		for _, tc := range cases {
			r := c.do(tc.method, tc.path, token, nil)
			if !want(t, r, 405, "method_not_allowed") {
				continue
			}
			allow := allowed(r)
			for _, m := range tc.allows {
				if !slices.Contains(allow, m) {
					t.Errorf("%s %s: Allow = %v, want it to list %s", tc.method, tc.path, allow, m)
				}
			}
			if slices.Contains(allow, tc.method) {
				t.Errorf("%s %s: Allow lists the rejected method", tc.method, tc.path)
			}
		}
	}
}

func TestRequestIDs(t *testing.T) {
	e := newEnv(t)

	r := e.withHeaders("GET", "/healthz", map[string]string{"X-Request-ID": "trace-abc.123"}, nil)
	if got := r.header.Get("X-Request-ID"); got != "trace-abc.123" {
		t.Errorf("a plain inbound ID was replaced by %q", got)
	}

	for _, bad := range []string{"bad id;DROP", strings.Repeat("a", 65)} {
		r = e.withHeaders("GET", "/healthz", map[string]string{"X-Request-ID": bad}, nil)
		if got := r.header.Get("X-Request-ID"); got == bad || len(got) != 16 {
			t.Errorf("inbound ID %q produced %q, want a generated 16-character ID", bad, got)
		}
	}

	a, b := e.do("GET", "/healthz", "", nil), e.do("GET", "/healthz", "", nil)
	if a.header.Get("X-Request-ID") == b.header.Get("X-Request-ID") {
		t.Error("two requests got the same generated ID")
	}

	// errors carry one too
	if e.do("GET", "/nope", "", nil).header.Get("X-Request-ID") == "" {
		t.Error("a 404 has no request ID")
	}
}

func TestDoctorDirectory(t *testing.T) {
	c := newClinic(t)

	names := func() []string {
		t.Helper()
		r := c.do("GET", "/doctors", c.alice.token, nil)
		must(t, r, 200)
		var got struct {
			Doctors []struct {
				ID   int32  `json:"id"`
				Name string `json:"name"`
			} `json:"doctors"`
		}
		r.decode(t, &got)
		for _, marker := range []string{"@", "dr_house", "username", "email"} {
			if strings.Contains(string(r.body), marker) {
				t.Errorf("the directory exposes %q: %s", marker, r.body)
			}
		}
		out := make([]string, len(got.Doctors))
		for i, d := range got.Doctors {
			out[i] = d.Name
		}
		return out
	}
	sorted := func(s ...string) []string {
		slices.Sort(s)
		return s
	}

	// doctors with a schedule, ordered by last name; the administrator and patients are not listed
	if got := names(); !slices.Equal(got, []string{"Allison Adams", "Gregory House"}) {
		t.Fatalf("directory = %v", got)
	}

	// a new doctor appears once they have a schedule
	cuddy := c.createDoctor(c.admin.token, "dr_cuddy", "Lisa", "Cuddy", false)
	if got := names(); len(got) != 2 {
		t.Errorf("a doctor without a schedule is listed: %v", got)
	}
	must(t, c.do("PUT", schedulePath(cuddy.id, 2), cuddy.token, lunchShift), 200)
	if got := sorted(names()...); !slices.Equal(got, sorted("Allison Adams", "Gregory House", "Lisa Cuddy")) {
		t.Errorf("directory after Dr Cuddy's schedule = %v", got)
	}

	// so does the administrator, once they take one
	must(t, c.do("PUT", schedulePath(c.admin.id, 2), c.admin.token, lunchShift), 200)
	if got := names(); !slices.Contains(got, "Clinic Administrator") {
		t.Errorf("directory = %v, want the administrator once they have a schedule", got)
	}

	// a deleted doctor leaves
	c.exec("UPDATE users SET deleted_at = now() WHERE user_id = $1", c.adams.id)
	if got := names(); slices.Contains(got, "Allison Adams") {
		t.Errorf("a deleted doctor is still listed: %v", got)
	}
}
