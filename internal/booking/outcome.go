package booking

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"clinic/internal/apperr"
	"clinic/internal/auth"
	"clinic/internal/store"
)

const maxNotesLength = 500

// MarkOutcome records whether a started appointment was completed or missed.
// Only the appointment's own doctor may do it, and only once, from the booked state.
func (s *Service) MarkOutcome(ctx context.Context, p auth.Principal, appointmentID int32,
	outcome store.AppointmentStatus, notes string) error {

	if !p.IsDoctor() {
		return apperr.ErrForbidden
	}
	if outcome != store.AppointmentStatusCompleted && outcome != store.AppointmentStatusNoShow {
		return &apperr.ValidationError{Problems: map[string]string{"outcome": "must be completed or no_show"}}
	}
	notes = strings.TrimSpace(notes)
	if utf8.RuneCountInString(notes) > maxNotesLength {
		return &apperr.ValidationError{Problems: map[string]string{"notes": "must be at most 500 characters"}}
	}

	// One conditional UPDATE decides: ownership, state and time are all in the WHERE.
	// Concurrent calls serialise on the row lock; the loser re-checks status and matches nothing.
	n, err := s.store.MarkAppointmentOutcome(ctx, store.MarkAppointmentOutcomeParams{
		Status:        outcome,
		Notes:         pgtype.Text{String: notes, Valid: notes != ""},
		AppointmentID: appointmentID,
		DoctorID:      p.UserID,
	})
	if err != nil {
		return fmt.Errorf("mark outcome: %w", err)
	}
	if n == 1 {
		return nil
	}

	// Nothing changed: look up why, only to give a useful error.
	a, err := s.store.GetDoctorAppointment(ctx, store.GetDoctorAppointmentParams{
		AppointmentID: appointmentID,
		DoctorID:      p.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound // unknown id or another doctor's: indistinguishable on purpose
	}
	if err != nil {
		return fmt.Errorf("get appointment: %w", err)
	}

	switch a.Status {
	case store.AppointmentStatusBooked:
		return fmt.Errorf("%w: appointment has not started", ErrNotMarkable)
	case store.AppointmentStatusCancelled:
		return fmt.Errorf("%w: appointment was cancelled", ErrNotMarkable)
	default:
		return fmt.Errorf("%w: already recorded as %s", ErrNotMarkable, a.Status)
	}
}

// PendingOutcomes lists the calling doctor's booked appointments that have already
// started and still need an outcome, oldest first. limit defaults to 50, capped at 100.
func (s *Service) PendingOutcomes(ctx context.Context, p auth.Principal, limit int) ([]DoctorAppointment, error) {
	if !p.IsDoctor() {
		return nil, apperr.ErrForbidden
	}
	if limit <= 0 {
		limit = defaultPageSize
	}
	limit = min(limit, maxPageSize)

	rows, err := s.store.ListPendingOutcomes(ctx, store.ListPendingOutcomesParams{
		DoctorID: p.UserID,
		PageSize: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list pending outcomes: %w", err)
	}

	out := make([]DoctorAppointment, len(rows))
	for i, r := range rows {
		out[i] = DoctorAppointment{
			ID:          r.AppointmentID,
			PatientID:   r.PatientID,
			PatientName: store.FullName(r.PatientFirstname, r.PatientLastname),
			Start:       r.AppointmentStart.In(s.loc),
			Status:      r.Status,
			Notes:       r.Notes.String,
		}
	}
	return out, nil
}
