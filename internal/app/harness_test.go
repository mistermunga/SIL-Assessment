package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"clinic/internal/app"
	"clinic/internal/auth"
	"clinic/internal/config"
	"clinic/internal/testutil"
)

const (
	testSecret    = "integration-test-secret-0123456789abcdef"
	adminPassword = "admin-password-123"
	userPassword  = "correct-horse-battery"
)

var clinicTZ = time.FixedZone("EAT", 3*60*60)

var lunchShift = map[string]string{
	"start": "09:00", "end": "17:00", "break_start": "12:00", "break_end": "13:00",
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testConfig() config.Config {
	return config.Config{
		JWTSecret:      []byte(testSecret),
		ClinicTZ:       clinicTZ,
		AccessTokenTTL: 15 * time.Minute,
		AdminUsername:  "administrator",
		AdminEmail:     "admin@clinic.local",
		AdminPassword:  adminPassword,
	}
}

// ---- wire format ----
// Declared here instead of imported, so the tests pin the JSON that clients see.

type userBody struct {
	ID        int32     `json:"id"`
	Username  string    `json:"username"`
	Email     string    `json:"email"`
	Firstname string    `json:"firstname"`
	Lastname  string    `json:"lastname"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

type tokenBody struct {
	Token     string    `json:"token"`
	TokenType string    `json:"token_type"`
	ExpiresAt time.Time `json:"expires_at"`
}

type registrationBody struct {
	User      userBody  `json:"user"`
	Token     string    `json:"token"`
	TokenType string    `json:"token_type"`
	ExpiresAt time.Time `json:"expires_at"`
}

type apptBody struct {
	ID        int32     `json:"id"`
	DoctorID  int32     `json:"doctor_id"`
	PatientID int32     `json:"patient_id"`
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Status    string    `json:"status"`
	Notes     string    `json:"notes"`
}

type patientEntryBody struct {
	ID         int32     `json:"id"`
	DoctorID   int32     `json:"doctor_id"`
	DoctorName string    `json:"doctor_name"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Status     string    `json:"status"`
	Notes      string    `json:"notes"`
}

type doctorEntryBody struct {
	ID          int32     `json:"id"`
	PatientID   int32     `json:"patient_id"`
	PatientName string    `json:"patient_name"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	Status      string    `json:"status"`
	Notes       string    `json:"notes"`
}

type slotBody struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type availabilityBody struct {
	DoctorID int32      `json:"doctor_id"`
	Date     string     `json:"date"`
	Slots    []slotBody `json:"slots"`
}

type shiftBody struct {
	Weekday    int    `json:"weekday"`
	Start      string `json:"start"`
	End        string `json:"end"`
	BreakStart string `json:"break_start"`
	BreakEnd   string `json:"break_end"`
}

type errEnvelope struct {
	Error struct {
		Code     string            `json:"code"`
		Message  string            `json:"message"`
		Problems map[string]string `json:"problems"`
	} `json:"error"`
}

// ---- the running application ----

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	conf   config.Config
	url    string
	client *http.Client
}

// newEnv empties the test database, builds the application over it exactly as main does
// (which seeds the administrator), and serves it on a local port.
func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testutil.NewPool(t)
	conf := testConfig()

	handler, err := app.New(context.Background(), conf, pool, quietLogger())
	if err != nil {
		t.Fatalf("build the application: %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	transport := &http.Transport{MaxIdleConnsPerHost: 32}
	t.Cleanup(transport.CloseIdleConnections)

	return &env{
		t: t, pool: pool, conf: conf, url: srv.URL,
		client: &http.Client{
			Transport:     transport,
			Timeout:       30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

type resp struct {
	status int
	header http.Header
	body   []byte
}

func (r resp) decode(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("cannot decode the response: %v\nbody: %s", err, r.body)
	}
}

func (r resp) errEnv() errEnvelope {
	var e errEnvelope
	_ = json.Unmarshal(r.body, &e)
	return e
}

// allowed returns the methods listed in a 405's Allow header.
func allowed(r resp) []string {
	var out []string
	for _, m := range strings.Split(r.header.Get("Allow"), ",") {
		out = append(out, strings.TrimSpace(m))
	}
	return out
}

// send performs one request. body may be nil, a string (sent as is), or a value (sent as JSON).
// It never calls FailNow, so it is safe to use from goroutines.
func (e *env) send(method, path string, headers map[string]string, body any) (resp, error) {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			return resp{}, fmt.Errorf("encode request body: %w", err)
		}
		rd = bytes.NewReader(raw)
	}

	req, err := http.NewRequest(method, e.url+path, rd)
	if err != nil {
		return resp{}, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := e.client.Do(req)
	if err != nil {
		return resp{}, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return resp{}, err
	}

	r := resp{status: res.StatusCode, header: res.Header, body: raw}
	e.check(method, path, r)
	return r, nil
}

// check holds the rules every response must follow, whatever the endpoint.
func (e *env) check(method, path string, r resp) {
	e.t.Helper()
	label := method + " " + path

	if r.header.Get("X-Request-ID") == "" {
		e.t.Errorf("%s: no X-Request-ID header", label)
	}
	for _, secret := range []string{"$2a$", "$2b$", adminPassword, userPassword, testSecret} {
		if bytes.Contains(r.body, []byte(secret)) {
			e.t.Errorf("%s: the response contains %q", label, secret)
		}
	}
	if r.status == http.StatusNoContent {
		if len(r.body) != 0 {
			e.t.Errorf("%s: a 204 response has a body", label)
		}
		return
	}
	if ct := r.header.Get("Content-Type"); ct != "application/json" {
		e.t.Errorf("%s: Content-Type = %q, want application/json (body %.100s)", label, ct, r.body)
	}
	if r.status < 400 {
		return
	}

	eb := r.errEnv()
	if eb.Error.Code == "" || eb.Error.Message == "" {
		e.t.Errorf("%s: %d is not in the error format: %s", label, r.status, r.body)
	}
	if r.status == http.StatusUnauthorized && r.header.Get("WWW-Authenticate") != "Bearer" {
		e.t.Errorf("%s: a 401 without WWW-Authenticate: Bearer", label)
	}
	if r.status == http.StatusMethodNotAllowed && r.header.Get("Allow") == "" {
		e.t.Errorf("%s: a 405 without an Allow header", label)
	}
}

func bearer(token string) map[string]string {
	if token == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + token}
}

// do sends a request as the holder of token (empty means no Authorization header).
func (e *env) do(method, path, token string, body any) resp {
	e.t.Helper()
	r, err := e.send(method, path, bearer(token), body)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	return r
}

// try is do for goroutines: a transport failure is reported but does not stop the caller.
func (e *env) try(method, path, token string, body any) resp {
	e.t.Helper()
	r, err := e.send(method, path, bearer(token), body)
	if err != nil {
		e.t.Errorf("%s %s: %v", method, path, err)
	}
	return r
}

func (e *env) withHeaders(method, path string, headers map[string]string, body any) resp {
	e.t.Helper()
	r, err := e.send(method, path, headers, body)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	return r
}

// must stops the test unless the response has the given status.
func must(t *testing.T, r resp, status int) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status = %d, want %d (body %s)", r.status, status, r.body)
	}
}

// want reports a mismatch without stopping. A non-empty code is also compared
// against the error code. It returns whether the response matched.
func want(t *testing.T, r resp, status int, code string) bool {
	t.Helper()
	if r.status != status {
		t.Errorf("status = %d, want %d (body %s)", r.status, status, r.body)
		return false
	}
	if code != "" {
		if got := r.errEnv().Error.Code; got != code {
			t.Errorf("error code = %q, want %q (body %s)", got, code, r.body)
			return false
		}
	}
	return true
}

// ---- data helpers ----

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("exec %q: %v", sql, err)
	}
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("query %q: %v", sql, err)
	}
	return n
}

func (e *env) userID(username string) int32 {
	e.t.Helper()
	var id int32
	err := e.pool.QueryRow(context.Background(),
		"SELECT user_id FROM users WHERE username = $1", username).Scan(&id)
	if err != nil {
		e.t.Fatalf("find user %q: %v", username, err)
	}
	return id
}

// tokenFor signs a valid token for a user without going through login.
func (e *env) tokenFor(userID int32) string {
	e.t.Helper()
	tok, _, err := auth.NewTokenManager(e.conf.JWTSecret, time.Hour).Issue(userID)
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

// login signs in over HTTP and returns the token.
func (e *env) login(username, password string) string {
	e.t.Helper()
	r := e.do("POST", "/login", "", map[string]string{"username": username, "password": password})
	must(e.t, r, 200)
	var tok tokenBody
	r.decode(e.t, &tok)
	return tok.Token
}

type person struct {
	id    int32
	name  string
	token string
}

// addDoctor and addPatient insert accounts directly (no password hashing) and mint a token.
func (e *env) addDoctor(username, first, last string) person {
	e.t.Helper()
	id := testutil.NewDoctor(e.t, e.pool, username)
	e.exec("UPDATE users SET firstname = $2, lastname = $3 WHERE user_id = $1", id, first, last)
	return person{id: id, name: username, token: e.tokenFor(id)}
}

func (e *env) addPatient(username, first, last string) person {
	e.t.Helper()
	id := testutil.NewPatient(e.t, e.pool, username)
	e.exec("UPDATE users SET firstname = $2, lastname = $3 WHERE user_id = $1", id, first, last)
	return person{id: id, name: username, token: e.tokenFor(id)}
}

// withLunchShift gives a doctor Monday 09:00-17:00 with a 12:00-13:00 break, by SQL.
func (e *env) withLunchShift(doctorID int32) {
	e.t.Helper()
	e.exec(`INSERT INTO doctor_schedules (doctor_id, weekday, start_time, end_time, break_start, break_end)
		VALUES ($1, 1, '09:00', '17:00', '12:00', '13:00')`, doctorID)
}

// createDoctor goes through POST /doctors; the returned token is minted, not logged in.
func (e *env) createDoctor(adminToken, username, first, last string, isAdmin bool) person {
	e.t.Helper()
	r := e.do("POST", "/doctors", adminToken, map[string]any{
		"username": username, "email": username + "@example.com", "password": userPassword,
		"firstname": first, "lastname": last, "is_admin": isAdmin,
	})
	must(e.t, r, 201)
	var u userBody
	r.decode(e.t, &u)
	return person{id: u.ID, name: username, token: e.tokenFor(u.ID)}
}

// registerPatient goes through POST /patients and keeps the token it returns.
func (e *env) registerPatient(username, first, last string) person {
	e.t.Helper()
	r := e.do("POST", "/patients", "", map[string]string{
		"username": username, "email": username + "@example.com", "password": userPassword,
		"firstname": first, "lastname": last,
	})
	must(e.t, r, 201)
	var reg registrationBody
	r.decode(e.t, &reg)
	return person{id: reg.User.ID, name: username, token: reg.Token}
}

// pastAppointment inserts an appointment that started daysAgo days ago.
func (e *env) pastAppointment(doctorID, patientID int32, daysAgo int, status string) int32 {
	e.t.Helper()
	start := time.Now().UTC().Truncate(time.Hour).AddDate(0, 0, -daysAgo)
	var id int32
	err := e.pool.QueryRow(context.Background(), `
		INSERT INTO appointments (doctor_id, patient_id, appointment_start, status)
		VALUES ($1, $2, $3, $4::appointment_status) RETURNING appointment_id`,
		doctorID, patientID, start, status).Scan(&id)
	if err != nil {
		e.t.Fatalf("insert a past appointment: %v", err)
	}
	return id
}

// ---- time and path helpers ----

func nextMonday() time.Time { return testutil.NextDay(clinicTZ, time.Monday) }

func clock(day time.Time, h, m int) time.Time {
	return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

func stamp(day time.Time, h, m int) string { return clock(day, h, m).Format(time.RFC3339) }

func dateStr(day time.Time) string { return day.Format("2006-01-02") }

func scheduleRoot(doctorID int32) string { return fmt.Sprintf("/doctors/%d/schedule", doctorID) }

func schedulePath(doctorID int32, weekday int) string {
	return fmt.Sprintf("/doctors/%d/schedule/%d", doctorID, weekday)
}

func availabilityPath(doctorID int32, day time.Time) string {
	return fmt.Sprintf("/doctors/%d/availability?date=%s", doctorID, dateStr(day))
}

func agendaPath(doctorID int32, day time.Time) string {
	return fmt.Sprintf("/doctors/%d/appointments?date=%s", doctorID, dateStr(day))
}

func apptPath(id int32, action string) string { return fmt.Sprintf("/appointments/%d/%s", id, action) }

// ---- fixture: a seeded clinic ----

// clinic has the seeded administrator, two doctors who work Mondays 09:00-17:00
// with a 12:00-13:00 break, and two patients.
type clinic struct {
	*env
	monday                          time.Time
	admin, house, adams, alice, bob person
}

func newClinic(t *testing.T) *clinic {
	t.Helper()
	e := newEnv(t)
	c := &clinic{env: e, monday: nextMonday()}

	c.admin = person{id: e.userID("administrator"), name: "administrator"}
	c.admin.token = e.tokenFor(c.admin.id)
	c.house = e.addDoctor("dr_house", "Gregory", "House")
	c.adams = e.addDoctor("dr_adams", "Allison", "Adams")
	for _, d := range []person{c.house, c.adams} {
		must(t, e.do("PUT", schedulePath(d.id, 1), d.token, lunchShift), 200)
	}
	c.alice = e.addPatient("alice", "Alice", "Lee")
	c.bob = e.addPatient("bob", "Bob", "Ray")
	return c
}

func (c *clinic) bookAt(p person, doctorID int32, start string) resp {
	return c.do("POST", "/appointments", p.token, map[string]any{"doctor_id": doctorID, "start": start})
}

func (c *clinic) book(p person, doctorID int32, h, m int) resp {
	return c.bookAt(p, doctorID, stamp(c.monday, h, m))
}

func (c *clinic) mustBook(p person, doctorID int32, h, m int) apptBody {
	c.t.Helper()
	r := c.book(p, doctorID, h, m)
	must(c.t, r, 201)
	var a apptBody
	r.decode(c.t, &a)
	return a
}

func (c *clinic) cancel(p person, id int32, reason string) resp {
	return c.do("PATCH", apptPath(id, "cancel"), p.token, map[string]string{"reason": reason})
}

func (c *clinic) slots(doctorID int32) []slotBody {
	c.t.Helper()
	r := c.do("GET", availabilityPath(doctorID, c.monday), c.alice.token, nil)
	must(c.t, r, 200)
	var av availabilityBody
	r.decode(c.t, &av)
	return av.Slots
}

func hasSlot(slots []slotBody, at time.Time) bool {
	for _, s := range slots {
		if s.Start.Equal(at) {
			return true
		}
	}
	return false
}

func (c *clinic) mine(p person, query string) []patientEntryBody {
	c.t.Helper()
	r := c.do("GET", "/appointments"+query, p.token, nil)
	must(c.t, r, 200)
	var got struct {
		Appointments []patientEntryBody `json:"appointments"`
	}
	r.decode(c.t, &got)
	return got.Appointments
}

func ids(es []patientEntryBody) []int32 {
	out := make([]int32, len(es))
	for i, e := range es {
		out[i] = e.ID
	}
	return out
}

// doctorList decodes the appointments of an agenda or pending-outcomes response.
func (c *clinic) doctorList(r resp) []doctorEntryBody {
	c.t.Helper()
	must(c.t, r, 200)
	var got struct {
		Appointments []doctorEntryBody `json:"appointments"`
	}
	r.decode(c.t, &got)
	return got.Appointments
}

func doctorIDs(es []doctorEntryBody) []int32 {
	out := make([]int32, len(es))
	for i, e := range es {
		out[i] = e.ID
	}
	return out
}
