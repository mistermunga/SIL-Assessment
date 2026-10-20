package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"clinic/internal/apperr"
)

const (
	dateLayout    = "2006-01-02"
	timestampHint = "must be an RFC 3339 timestamp with a UTC offset, e.g. 2026-10-26T10:00:00+03:00"
)

// invalid builds a 422 for a single field.
func invalid(field, message string) error {
	return &apperr.ValidationError{Problems: map[string]string{field: message}}
}

// pathID reads a positive integer path value such as {id}.
func pathID(r *http.Request, name string) (int32, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 32)
	if err != nil || id <= 0 {
		return 0, badRequest("%s must be a positive integer", name)
	}
	return int32(id), nil
}

// dateParam reads a required YYYY-MM-DD query parameter as midnight in the clinic zone.
func dateParam(r *http.Request, name string, loc *time.Location) (time.Time, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return time.Time{}, invalid(name, "is required, as YYYY-MM-DD")
	}
	d, err := time.ParseInLocation(dateLayout, v, loc)
	if err != nil {
		return time.Time{}, invalid(name, "must be a date as YYYY-MM-DD")
	}
	return d, nil
}

// intParam reads an optional integer query parameter; absent means 0.
func intParam(r *http.Request, name string) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil {
		return 0, invalid(name, "must be an integer")
	}
	return int(n), nil
}

// boolParam reads an optional boolean query parameter; absent means false.
func boolParam(r *http.Request, name string) (bool, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, invalid(name, "must be true or false")
	}
	return b, nil
}

func parseTimestamp(v string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, v)
	return t, err == nil
}
