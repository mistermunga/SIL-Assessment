package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"
	"unicode/utf8"
)

const (
	requestIDHeader = "X-Request-ID"
	maxRequestID    = 64
	maxLoggedPath   = 200
)

// requestInfo is filled in as a request moves through the server and read by the
// access log when it finishes. It belongs to one request and one goroutine.
type requestInfo struct {
	id     string
	route  string // the matched pattern; empty when nothing matched
	quiet  bool
	userID int32 // set once the caller is authenticated
}

type infoKey struct{}

// infoFrom returns the request's info, or nil outside the logging middleware.
func infoFrom(ctx context.Context) *requestInfo {
	info, _ := ctx.Value(infoKey{}).(*requestInfo)
	return info
}

func requestID(ctx context.Context) string {
	if info := infoFrom(ctx); info != nil {
		return info.id
	}
	return ""
}

// statusWriter remembers the status and size of the response as it is written.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// logRequests writes one line per request when it finishes, and tags the response
// and every other log line of the request with an ID.
//
// Logged: ID, method, matched route, path, status, duration, response size, and the
// caller's user ID. Not logged: headers (so no tokens), query strings, bodies
// (so no passwords) and client addresses (the reverse proxy's log has those).
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// An ID from the proxy is kept when it is short and plain, so both logs match.
		id := r.Header.Get(requestIDHeader)
		if !validRequestID(id) {
			id = newRequestID()
		}
		w.Header().Set(requestIDHeader, id)

		info := &requestInfo{id: id}
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), infoKey{}, info)))

		status := sw.status
		if status == 0 {
			status = http.StatusOK // nothing was written, which net/http sends as 200
		}
		route := info.route
		if route == "" {
			route = "unmatched"
		}

		attrs := []slog.Attr{
			slog.String("request_id", id),
			slog.String("method", r.Method),
			slog.String("route", route),
			slog.String("path", clip(r.URL.Path, maxLoggedPath)),
			slog.Int("status", status),
			slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000),
			slog.Int64("bytes", sw.bytes),
		}
		if info.userID != 0 {
			attrs = append(attrs, slog.Int("user_id", int(info.userID)))
		}
		s.log.LogAttrs(r.Context(), levelFor(status, info.quiet), "request", attrs...)
	})
}

func levelFor(status int, quiet bool) slog.Level {
	switch {
	case status >= 500:
		return slog.LevelError
	case status >= 400:
		return slog.LevelWarn
	case quiet:
		return slog.LevelDebug
	default:
		return slog.LevelInfo
	}
}

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:]) // crypto/rand does not fail in practice
	return hex.EncodeToString(b[:])
}

// validRequestID accepts short IDs made of letters, digits, '-', '_' and '.'.
func validRequestID(v string) bool {
	if v == "" || len(v) > maxRequestID {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// clip shortens s to at most n bytes without splitting a character.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
