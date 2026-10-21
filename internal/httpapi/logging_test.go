package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func captureLogger(level slog.Level) (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level})), buf
}

func loggedHandler(log *slog.Logger, modules ...Module) http.Handler {
	return NewServer(authnFor(), log, modules...).Handler()
}

// logEntries decodes every JSON log line written so far.
func logEntries(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("log output is not JSON lines: %v\n%s", err, buf)
		}
		out = append(out, m)
	}
	return out
}

// oneAccessLine returns the single per-request summary line.
func oneAccessLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, e := range logEntries(t, buf) {
		if e["msg"] == "request" {
			lines = append(lines, e)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("access lines = %d, want 1\n%s", len(lines), buf)
	}
	return lines[0]
}

func TestAccessLogFields(t *testing.T) {
	log, buf := captureLogger(slog.LevelDebug)
	rec := do(loggedHandler(log, testModule{}), "GET", "/open", "", "")

	line := oneAccessLine(t, buf)
	if line["level"] != "INFO" || line["method"] != "GET" || line["route"] != "GET /open" ||
		line["path"] != "/open" || line["status"] != float64(200) {
		t.Errorf("line = %v", line)
	}
	if id, _ := line["request_id"].(string); id == "" || id != rec.Header().Get("X-Request-ID") {
		t.Errorf("request_id = %v, response header = %q", line["request_id"], rec.Header().Get("X-Request-ID"))
	}
	if d, ok := line["duration_ms"].(float64); !ok || d < 0 {
		t.Errorf("duration_ms = %v", line["duration_ms"])
	}
	if b, _ := line["bytes"].(float64); b <= 0 {
		t.Errorf("bytes = %v, want the response size", line["bytes"])
	}
}

func TestAccessLogRecordsTheCaller(t *testing.T) {
	cases := []struct {
		name, path, token string
		status            int
		user              float64 // 0: the field must be absent
	}{
		{"authenticated", "/doctor", "doc-token", 200, float64(doctorP.UserID)},
		{"forbidden still names the caller", "/admin", "doc-token", 403, float64(doctorP.UserID)},
		{"bad token", "/any", "nope", 401, 0},
		{"open route", "/open", "", 200, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			log, buf := captureLogger(slog.LevelDebug)
			do(loggedHandler(log, testModule{}), "GET", c.path, c.token, "")

			line := oneAccessLine(t, buf)
			if line["status"] != float64(c.status) {
				t.Errorf("status = %v, want %d", line["status"], c.status)
			}
			got, present := line["user_id"]
			if present != (c.user != 0) || (present && got != c.user) {
				t.Errorf("user_id = %v (present %v), want %v", got, present, c.user)
			}
		})
	}
}

func TestAccessLogLevels(t *testing.T) {
	cases := []struct{ name, path, level string }{
		{"success", "/open", "INFO"},
		{"client error", "/any", "WARN"}, // 401: no token
		{"unknown route", "/nope", "WARN"},
		{"server error", "/boom", "ERROR"},
		{"panic", "/panic", "ERROR"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			log, buf := captureLogger(slog.LevelDebug)
			do(loggedHandler(log, testModule{}), "GET", c.path, "", "")
			if got := oneAccessLine(t, buf)["level"]; got != c.level {
				t.Errorf("level = %v, want %s", got, c.level)
			}
		})
	}
}

func TestAccessLogForUnmatchedRoutes(t *testing.T) {
	log, buf := captureLogger(slog.LevelDebug)
	h := loggedHandler(log, testModule{})

	do(h, "GET", "/nope", "", "")
	if line := oneAccessLine(t, buf); line["route"] != "unmatched" || line["status"] != float64(404) {
		t.Errorf("404 line = %v", line)
	}

	log, buf = captureLogger(slog.LevelDebug)
	do(loggedHandler(log, testModule{}), "DELETE", "/open", "", "")
	if line := oneAccessLine(t, buf); line["route"] != "unmatched" || line["status"] != float64(405) {
		t.Errorf("405 line = %v", line)
	}
}

func TestQuietRoutesLogAtDebug(t *testing.T) {
	healthy := func(log *slog.Logger) http.Handler {
		return NewServer(authnFor(), log, NewHealthModule(fakePinger{})).Handler()
	}

	log, buf := captureLogger(slog.LevelInfo)
	do(healthy(log), "GET", "/healthz", "", "")
	if buf.Len() != 0 {
		t.Errorf("a healthy check was logged at info level: %s", buf)
	}

	log, buf = captureLogger(slog.LevelDebug)
	do(healthy(log), "GET", "/healthz", "", "")
	if line := oneAccessLine(t, buf); line["level"] != "DEBUG" {
		t.Errorf("level = %v, want DEBUG", line["level"])
	}

	// a failing check is never hidden
	log, buf = captureLogger(slog.LevelInfo)
	down := NewServer(authnFor(), log, NewHealthModule(fakePinger{err: errors.New("refused")})).Handler()
	do(down, "GET", "/healthz", "", "")
	if line := oneAccessLine(t, buf); line["level"] != "ERROR" || line["status"] != float64(503) {
		t.Errorf("failing check line = %v, want ERROR 503", line)
	}
}

func TestRequestIDHandling(t *testing.T) {
	cases := []struct {
		name, in string
		keep     bool
	}{
		{"none sent", "", false},
		{"plain id", "abc-123_DEF.9", true},
		{"with spaces", "not valid", false},
		{"too long", strings.Repeat("a", 65), false},
		{"punctuation", "id;drop table", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			log, _ := captureLogger(slog.LevelDebug)
			req := httptest.NewRequest("GET", "/open", nil)
			if c.in != "" {
				req.Header.Set("X-Request-ID", c.in)
			}
			rec := httptest.NewRecorder()
			loggedHandler(log, testModule{}).ServeHTTP(rec, req)

			got := rec.Header().Get("X-Request-ID")
			switch {
			case c.keep && got != c.in:
				t.Errorf("X-Request-ID = %q, want the sent value kept", got)
			case !c.keep && (got == c.in || len(got) != 16):
				t.Errorf("X-Request-ID = %q, want a generated 16-character ID", got)
			}
		})
	}
}

func TestLogsNeverContainCredentialsOrQueryStrings(t *testing.T) {
	log, buf := captureLogger(slog.LevelDebug)
	h := loggedHandler(log, testModule{})

	do(h, "GET", "/any?x=query-secret", "pat-token", "")
	do(h, "POST", "/echo", "pat-token", `{"name":"body-secret"}`)

	for _, secret := range []string{"pat-token", "query-secret", "body-secret"} {
		if strings.Contains(buf.String(), secret) {
			t.Errorf("log output contains %q:\n%s", secret, buf)
		}
	}
}

func TestLongPathsAreClipped(t *testing.T) {
	log, buf := captureLogger(slog.LevelDebug)
	do(loggedHandler(log, testModule{}), "GET", "/"+strings.Repeat("a", 5000), "", "")

	path, _ := oneAccessLine(t, buf)["path"].(string)
	if len(path) == 0 || len(path) > maxLoggedPath+len("…") {
		t.Errorf("logged path is %d bytes, want at most %d", len(path), maxLoggedPath+len("…"))
	}
}
