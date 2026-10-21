package app_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"clinic/internal/app"
	"clinic/internal/auth"
)

func TestRegisterPatient(t *testing.T) {
	e := newEnv(t)
	body := func(username, email string) map[string]string {
		return map[string]string{
			"username": username, "email": email, "password": userPassword,
			"firstname": "Carol", "lastname": "Xu",
		}
	}

	t.Run("creates the account and signs the patient in", func(t *testing.T) {
		r := e.do("POST", "/patients", "", body("  Carol_X ", "Carol@Example.com"))
		must(t, r, 201)
		var got registrationBody
		r.decode(t, &got)

		u := got.User
		if u.ID == 0 || u.Username != "carol_x" || u.Email != "carol@example.com" || u.Firstname != "Carol" ||
			u.Lastname != "Xu" || u.Role != "patient" || u.CreatedAt.IsZero() {
			t.Errorf("user = %+v", u)
		}
		if got.Token == "" || got.TokenType != "Bearer" || !got.ExpiresAt.After(time.Now()) {
			t.Errorf("token = %q %q %v", got.Token, got.TokenType, got.ExpiresAt)
		}
		if n := e.count("SELECT count(*) FROM patients WHERE patient_id = $1", u.ID); n != 1 {
			t.Errorf("patient rows = %d, want 1", n)
		}
		if n := e.count("SELECT count(*) FROM doctors WHERE doctor_id = $1", u.ID); n != 0 {
			t.Errorf("doctor rows = %d, want 0", n)
		}

		// the token works at once, and the normalised name signs in
		want(t, e.do("GET", "/appointments", got.Token, nil), 200, "")
		e.login("CAROL_X", userPassword)
	})

	t.Run("duplicates are conflicts and create nothing", func(t *testing.T) {
		e.registerPatient("dave", "Dave", "Dee")
		users := e.count("SELECT count(*) FROM users")

		want(t, e.do("POST", "/patients", "", body("dave", "other@example.com")), 409, "username_taken")
		want(t, e.do("POST", "/patients", "", body("DAVE", "other@example.com")), 409, "username_taken")
		want(t, e.do("POST", "/patients", "", body("dave2", "DAVE@example.com")), 409, "email_taken")

		if n := e.count("SELECT count(*) FROM users"); n != users {
			t.Errorf("users = %d, want %d", n, users)
		}
	})

	t.Run("invalid input is a 422 naming each field", func(t *testing.T) {
		r := e.do("POST", "/patients", "", map[string]string{"username": "ab", "email": "nope", "password": "short"})
		if want(t, r, 422, "validation_failed") {
			for _, f := range []string{"username", "email", "password"} {
				if r.errEnv().Error.Problems[f] == "" {
					t.Errorf("no problem reported for %q", f)
				}
			}
		}
		longPassword := strings.Repeat("x", 73)
		r = e.do("POST", "/patients", "", body("erin", "erin@example.com"))
		_ = r
		r = e.do("POST", "/patients", "", map[string]string{"username": "erin2", "email": "erin2@example.com", "password": longPassword})
		want(t, r, 422, "validation_failed")
	})

	t.Run("malformed or smuggled bodies are rejected", func(t *testing.T) {
		users := e.count("SELECT count(*) FROM users")
		bad := map[string]string{
			"empty":       "",
			"malformed":   `{"username":`,
			"wrong type":  `{"username":5}`,
			"role":        `{"username":"x","role":"admin"}`,
			"admin flag":  `{"username":"x","is_admin":true}`,
			"hash":        `{"username":"x","password_hash":"y"}`,
			"two objects": `{} {}`,
			"array":       `[]`,
		}
		for name, b := range bad {
			t.Run(name, func(t *testing.T) {
				want(t, e.do("POST", "/patients", "", b), 400, "bad_request")
			})
		}
		if n := e.count("SELECT count(*) FROM users"); n != users {
			t.Errorf("users = %d, want %d", n, users)
		}
	})
}

func TestLogin(t *testing.T) {
	e := newEnv(t)
	e.registerPatient("alice", "Alice", "Lee")
	e.registerPatient("gone", "Gone", "User")
	e.exec("UPDATE users SET deleted_at = now() WHERE username = 'gone'")

	t.Run("signs in with a normalised username", func(t *testing.T) {
		before := time.Now()
		r := e.do("POST", "/login", "", map[string]string{"username": " ALICE ", "password": userPassword})
		must(t, r, 200)
		var tok tokenBody
		r.decode(t, &tok)

		if tok.Token == "" || tok.TokenType != "Bearer" {
			t.Errorf("token = %q %q", tok.Token, tok.TokenType)
		}
		if d := tok.ExpiresAt.Sub(before); d < 14*time.Minute || d > 16*time.Minute {
			t.Errorf("expires in %v, want about 15 minutes", d)
		}
		want(t, e.do("GET", "/appointments", tok.Token, nil), 200, "")
	})

	t.Run("every failure looks the same", func(t *testing.T) {
		cases := map[string]map[string]string{
			"wrong password": {"username": "alice", "password": "not-the-password"},
			"unknown user":   {"username": "nobody", "password": userPassword},
			"deleted user":   {"username": "gone", "password": userPassword},
			"empty":          {"username": "", "password": ""},
		}
		message := ""
		for name, c := range cases {
			r := e.do("POST", "/login", "", c)
			want(t, r, 401, "invalid_credentials")
			if m := r.errEnv().Error.Message; message == "" {
				message = m
			} else if m != message {
				t.Errorf("%s: message %q differs from %q, which reveals more than it should", name, m, message)
			}
		}
	})

	t.Run("bad bodies are rejected", func(t *testing.T) {
		for name, b := range map[string]string{
			"empty":         "",
			"malformed":     `{"username":`,
			"wrong type":    `{"username":5}`,
			"unknown field": `{"username":"a","password":"b","role":"admin"}`,
		} {
			t.Run(name, func(t *testing.T) {
				want(t, e.do("POST", "/login", "", b), 400, "bad_request")
			})
		}
	})
}

func TestTokenValidation(t *testing.T) {
	e := newEnv(t)
	patient := e.addPatient("pat", "Pat", "Ient")

	mint := func(secret string, ttl time.Duration, userID int32) string {
		tok, _, err := auth.NewTokenManager([]byte(secret), ttl).Issue(userID)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	signed := func(method jwt.SigningMethod, key any, userID int32) string {
		claims := jwt.RegisteredClaims{
			Issuer:    "clinic",
			Subject:   strconv.Itoa(int(userID)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		}
		s, err := jwt.NewWithClaims(method, claims).SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	// a user row with no patient or doctor row has no role
	e.exec("INSERT INTO users (username, email, password_hash) VALUES ('orphan', 'orphan@example.com', 'x')")
	orphan := e.userID("orphan")

	rejected := map[string]string{
		"garbage":             "not-a-token",
		"wrong secret":        mint("a-different-secret-of-32-bytes!!!!!", time.Hour, patient.id),
		"expired":             mint(testSecret, -time.Minute, patient.id),
		"unknown user":        mint(testSecret, time.Hour, 999999),
		"user without a role": mint(testSecret, time.Hour, orphan),
		"algorithm none":      signed(jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, patient.id),
		"other algorithm":     signed(jwt.SigningMethodHS512, []byte(testSecret), patient.id),
	}
	for name, tok := range rejected {
		t.Run(name, func(t *testing.T) {
			want(t, e.do("GET", "/doctors", tok, nil), 401, "unauthorized")
		})
	}

	headers := []struct {
		name   string
		header string
		status int
	}{
		{"no header", "", 401},
		{"wrong scheme", "Basic " + patient.token, 401},
		{"scheme only", "Bearer", 401},
		{"lower-case scheme", "bearer " + patient.token, 200},
	}
	for _, h := range headers {
		t.Run(h.name, func(t *testing.T) {
			var hdr map[string]string
			if h.header != "" {
				hdr = map[string]string{"Authorization": h.header}
			}
			want(t, e.withHeaders("GET", "/doctors", hdr, nil), h.status, "")
		})
	}

	t.Run("a deleted account's token stops working at once", func(t *testing.T) {
		p := e.addPatient("soon_gone", "Soon", "Gone")
		want(t, e.do("GET", "/appointments", p.token, nil), 200, "")
		e.exec("UPDATE users SET deleted_at = now() WHERE user_id = $1", p.id)
		want(t, e.do("GET", "/appointments", p.token, nil), 401, "unauthorized")
	})
}

// Tokens carry only the user ID; the role is read from the database on every request.
func TestRolesAreReadPerRequest(t *testing.T) {
	c := newClinic(t)
	doctorBody := func(username string) map[string]string {
		return map[string]string{"username": username, "email": username + "@example.com", "password": userPassword}
	}

	// a demoted administrator loses admin routes on the next request
	second := c.createDoctor(c.admin.token, "second_admin", "Sec", "Admin", true)
	want(t, c.do("POST", "/doctors", second.token, doctorBody("made_by_second")), 201, "")
	c.exec("UPDATE doctors SET is_admin = false WHERE doctor_id = $1", second.id)
	want(t, c.do("POST", "/doctors", second.token, doctorBody("made_again")), 403, "forbidden")
	want(t, c.do("GET", scheduleRoot(second.id), second.token, nil), 200, "")

	// a promoted doctor gains them
	c.exec("UPDATE doctors SET is_admin = true WHERE doctor_id = $1", c.house.id)
	want(t, c.do("POST", "/doctors", c.house.token, doctorBody("made_by_house")), 201, "")

	// a deleted doctor is locked out and leaves the directory
	c.exec("UPDATE users SET deleted_at = now() WHERE user_id = $1", c.adams.id)
	want(t, c.do("GET", scheduleRoot(c.adams.id), c.adams.token, nil), 401, "unauthorized")
	r := c.do("GET", "/doctors", c.alice.token, nil)
	must(t, r, 200)
	if strings.Contains(string(r.body), "Adams") {
		t.Errorf("a deleted doctor is still listed: %s", r.body)
	}
}

func TestCreateDoctor(t *testing.T) {
	c := newClinic(t)
	body := func(username string, isAdmin bool) map[string]any {
		return map[string]any{
			"username": username, "email": username + "@example.com", "password": userPassword,
			"firstname": "New", "lastname": "Doctor", "is_admin": isAdmin,
		}
	}

	t.Run("only an administrator may create doctors", func(t *testing.T) {
		before := c.count("SELECT count(*) FROM doctors")
		want(t, c.do("POST", "/doctors", "", body("d_anon", false)), 401, "unauthorized")
		want(t, c.do("POST", "/doctors", c.alice.token, body("d_patient", false)), 403, "forbidden")
		want(t, c.do("POST", "/doctors", c.house.token, body("d_doctor", false)), 403, "forbidden")
		if n := c.count("SELECT count(*) FROM doctors"); n != before {
			t.Errorf("doctors = %d, want %d", n, before)
		}
	})

	t.Run("creates a doctor who can sign in", func(t *testing.T) {
		r := c.do("POST", "/doctors", c.admin.token, body("dr_new", false))
		must(t, r, 201)
		var u userBody
		r.decode(t, &u)
		if u.ID == 0 || u.Username != "dr_new" || u.Role != "doctor" || u.Firstname != "New" {
			t.Errorf("user = %+v", u)
		}
		if n := c.count("SELECT count(*) FROM doctors WHERE doctor_id = $1 AND NOT is_admin", u.ID); n != 1 {
			t.Errorf("plain doctor rows = %d, want 1", n)
		}
		token := c.login("dr_new", userPassword)
		want(t, c.do("GET", scheduleRoot(u.ID), token, nil), 200, "")
	})

	t.Run("administrators can create administrators", func(t *testing.T) {
		r := c.do("POST", "/doctors", c.admin.token, body("admin_two", true))
		must(t, r, 201)
		var u userBody
		r.decode(t, &u)
		if u.Role != "admin" {
			t.Errorf("role = %q, want admin", u.Role)
		}
		token := c.login("admin_two", userPassword)
		want(t, c.do("POST", "/doctors", token, body("made_by_two", false)), 201, "")
	})

	t.Run("conflicts and invalid input", func(t *testing.T) {
		want(t, c.do("POST", "/doctors", c.admin.token, body("dr_house", false)), 409, "username_taken")

		sameEmail := body("fresh_name", false)
		sameEmail["email"] = "dr_house@example.com"
		want(t, c.do("POST", "/doctors", c.admin.token, sameEmail), 409, "email_taken")

		r := c.do("POST", "/doctors", c.admin.token, map[string]string{"username": "x"})
		if want(t, r, 422, "validation_failed") && r.errEnv().Error.Problems["password"] == "" {
			t.Error("no problem reported for password")
		}
		for _, b := range []string{"", `{"username":`, `{"username":"x","role":"admin"}`, `{"username":"x","is_admin":"yes"}`} {
			want(t, c.do("POST", "/doctors", c.admin.token, b), 400, "bad_request")
		}
	})
}

func TestStartupSeedsTheAdministrator(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	t.Run("a restart neither duplicates the administrator nor changes the password", func(t *testing.T) {
		conf := e.conf
		conf.AdminPassword = "a-different-password-1"
		if _, err := app.New(ctx, conf, e.pool, quietLogger()); err != nil {
			t.Fatalf("restart: %v", err)
		}
		if n := e.count("SELECT count(*) FROM doctors WHERE is_admin"); n != 1 {
			t.Errorf("administrators = %d, want 1", n)
		}
		e.login("administrator", adminPassword)
		r := e.do("POST", "/login", "", map[string]string{"username": "administrator", "password": "a-different-password-1"})
		want(t, r, 401, "invalid_credentials")
	})

	t.Run("startup fails when no administrator can be created", func(t *testing.T) {
		// no active administrator remains, but the username stays taken
		e.exec("UPDATE users SET deleted_at = now() WHERE username = 'administrator'")
		if _, err := app.New(ctx, e.conf, e.pool, quietLogger()); err == nil {
			t.Error("want an error when the administrator's username is taken")
		}

		conf := e.conf
		conf.AdminUsername, conf.AdminEmail, conf.AdminPassword = "admin2", "admin2@clinic.local", "short"
		if _, err := app.New(ctx, conf, e.pool, quietLogger()); err == nil {
			t.Error("want an error for a weak administrator password")
		}

		conf.AdminPassword = adminPassword
		if _, err := app.New(ctx, conf, e.pool, quietLogger()); err != nil {
			t.Errorf("a valid replacement administrator was not seeded: %v", err)
		}
		e.login("admin2", adminPassword)
	})
}
