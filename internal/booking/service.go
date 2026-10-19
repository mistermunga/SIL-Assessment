package booking

import (
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"clinic/internal/store"
)

var (
	// ErrSlotUnavailable wraps a reason: off-grid, past, no shift, break, outside hours, leave.
	ErrSlotUnavailable  = errors.New("slot unavailable")
	ErrSlotTaken        = errors.New("slot taken")
	ErrPatientBusy      = errors.New("patient already has an appointment at that time")
	ErrNotFound         = errors.New("appointment not found")
	ErrAlreadyCancelled = errors.New("appointment already cancelled")
	ErrNotCancellable   = errors.New("appointment cannot be cancelled")
)

// Names of the partial unique indexes in the migrations.
const (
	constraintActiveSlot  = "uq_doctor_active_slot"
	constraintPatientSlot = "uq_patient_active_slot"
)

type Service struct {
	store *store.Store
	loc   *time.Location // clinic time zone
}

func NewService(s *store.Store, loc *time.Location) *Service {
	return &Service{store: s, loc: loc}
}

func unavailable(reason string) error {
	return fmt.Errorf("%w: %s", ErrSlotUnavailable, reason)
}

// dateOf returns the calendar date of t (in t's own location) for a date column.
func dateOf(t time.Time) pgtype.Date {
	return pgtype.Date{Time: time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), Valid: true}
}
