package booking

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"clinic/internal/apperr"
	"clinic/internal/auth"
	"clinic/internal/store"
)

const maxReasonLength = 500

// Cancel cancels the calling patient's own future booked appointment,
// recording the reason in the appointment's notes.
func (s *Service) Cancel(ctx context.Context, p auth.Principal, appointmentID int32, reason string) error {
	if !p.IsPatient() {
		return apperr.ErrForbidden
	}

	reason = strings.TrimSpace(reason)
	switch n := utf8.RuneCountInString(reason); {
	case n == 0:
		return &apperr.ValidationError{Problems: map[string]string{"reason": "is required"}}
	case n > maxReasonLength:
		return &apperr.ValidationError{Problems: map[string]string{"reason": "must be at most 500 characters"}}
	}

	// One conditional UPDATE decides: ownership, status and time are all in the WHERE.
	// Concurrent cancels serialise on the row lock; the loser re-checks status and matches nothing.
	n, err := s.store.CancelAppointment(ctx, store.CancelAppointmentParams{
		AppointmentID: appointmentID,
		PatientID:     p.UserID,
		Notes:         reason,
	})
	if err != nil {
		return fmt.Errorf("cancel appointment: %w", err)
	}
	if n == 1 {
		return nil
	}

	// Nothing changed: look up why, only to give a useful error.
	a, err := s.store.GetPatientAppointment(ctx, store.GetPatientAppointmentParams{
		AppointmentID: appointmentID,
		PatientID:     p.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound // unknown id or someone else's: indistinguishable on purpose
	}
	if err != nil {
		return fmt.Errorf("get appointment: %w", err)
	}

	switch a.Status {
	case store.AppointmentStatusCancelled:
		return ErrAlreadyCancelled
	case store.AppointmentStatusBooked:
		return fmt.Errorf("%w: already started", ErrNotCancellable)
	default:
		return fmt.Errorf("%w: appointment is %s", ErrNotCancellable, a.Status)
	}
}
