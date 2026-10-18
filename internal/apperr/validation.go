package apperr

import "fmt"

type ValidationError struct {
	Problems map[string]string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid input: %v", e.Problems)
}
