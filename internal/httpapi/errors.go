package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"

	"clinic/internal/apperr"
)

// HTTPError is an error that already knows its response. Handlers use it for
// request-level problems (bad body, bad path value) that have no domain sentinel.
type HTTPError struct {
	Status  int
	Code    string
	Message string // sent to the client
	Err     error  // not sent to client
}

func (e *HTTPError) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *HTTPError) Unwrap() error { return e.Err }

func badRequest(format string, args ...any) error {
	return &HTTPError{Status: http.StatusBadRequest, Code: "bad_request", Message: fmt.Sprintf(format, args...)}
}

// ErrorRule maps a domain error to a response. The message sent is the error's own text,
// so a sentinel may only be wrapped with text that is safe to show to clients.
type ErrorRule struct {
	Is     error
	Status int
	Code   string
}

func coreRules() []ErrorRule {
	return []ErrorRule{
		{Is: errUnauthorized, Status: http.StatusUnauthorized, Code: "unauthorized"},
		{Is: apperr.ErrForbidden, Status: http.StatusForbidden, Code: "forbidden"},
	}
}

type errorBody struct {
	Code     string            `json:"code"`
	Message  string            `json:"message"`
	Problems map[string]string `json:"problems,omitempty"`
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

var internalBody = errorBody{Code: "internal_error", Message: "internal error"}

// resolve picks the response for err. Anything unrecognised becomes a generic 500,
// so internal details never reach the client.
func (s *Server) resolve(err error) (int, errorBody) {
	if httpErr, ok := errors.AsType[*HTTPError](err); ok {
		return httpErr.Status, errorBody{Code: httpErr.Code, Message: httpErr.Message}
	}
	if invalid, ok := errors.AsType[*apperr.ValidationError](err); ok {
		return http.StatusUnprocessableEntity,
			errorBody{Code: "validation_failed", Message: "invalid input", Problems: invalid.Problems}
	}
	for _, rule := range s.rules {
		if errors.Is(err, rule.Is) {
			return rule.Status, errorBody{Code: rule.Code, Message: err.Error()}
		}
	}
	return http.StatusInternalServerError, internalBody
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, body := s.resolve(err)
	if status >= 500 {
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	}
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	s.log.Error("request failed", "request_id", requestID(r.Context()),
		"method", r.Method, "path", r.URL.Path, "err", err)
	_ = writeJSON(w, status, errorEnvelope{Error: body})
}

// recoverPanic turns a panic in any handler into a logged generic 500.
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler {
				panic(rec) // the server's own signal to abort a response
			}
			s.log.Error("panic in handler", "request_id", requestID(r.Context()),
				"method", r.Method, "path", r.URL.Path,
				"panic", rec, "stack", string(debug.Stack()))
			_ = writeJSON(w, http.StatusInternalServerError, errorEnvelope{Error: internalBody})
		}()
		next.ServeHTTP(w, r)
	})
}
