package apperr

import "errors"

// ErrForbidden states the caller's role may not perform the operation.
var ErrForbidden = errors.New("forbidden")
