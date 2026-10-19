package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const maxBodyBytes = 1 << 20 // 1 MiB

// writeJSON encodes v before writing anything, so an encoding failure can still become a 500.
func writeJSON(w http.ResponseWriter, status int, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode response: %w", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
	return nil
}

func noContent(w http.ResponseWriter) error {
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// readJSON decodes exactly one JSON value into dst. It caps the body size, rejects
// unknown fields, and rejects trailing data.
func readJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		var syntax *json.SyntaxError
		var wrongType *json.UnmarshalTypeError
		switch {
		case errors.As(err, &tooLarge):
			return &HTTPError{Status: http.StatusRequestEntityTooLarge, Code: "payload_too_large",
				Message: "request body too large"}
		case errors.Is(err, io.EOF):
			return badRequest("request body is empty")
		case errors.As(err, &syntax), errors.Is(err, io.ErrUnexpectedEOF):
			return badRequest("malformed JSON")
		case errors.As(err, &wrongType):
			return badRequest("field %q has the wrong type", wrongType.Field)
		default: // e.g. unknown field "x"
			return badRequest("%s", strings.TrimPrefix(err.Error(), "json: "))
		}
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return badRequest("body must contain a single JSON object")
	}
	return nil
}
