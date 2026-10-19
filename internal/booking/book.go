package booking

import (
	"context"
	"fmt"
	"time"

	"clinic/internal/apperr"
	"clinic/internal/auth"
	"clinic/internal/store"
)

// Book reserves the slot starting at start for the calling patient.
func (s *Service) Book(ctx context.Context, p auth.Principal, doctorID int32, start time.Time) (store.Appointment, error) {
	if !p.IsPatient() {
		return store.Appointment{}, apperr.ErrForbidden
	}
	if err := s.checkStart(start); err != nil {
		return store.Appointment{}, err
	}

	var appt store.Appointment
	err := s.store.InTx(ctx, func(q *store.Queries) error {
		if err := lockDoctorShared(ctx, q, doctorID); err != nil {
			return err
		}
		if err := s.checkSlot(ctx, q, doctorID, start); err != nil {
			return err
		}
		a, err := q.InsertAppointment(ctx, store.InsertAppointmentParams{
			DoctorID:         doctorID,
			PatientID:        p.UserID,
			AppointmentStart: start,
		})
		if err != nil {
			return fmt.Errorf("insert appointment: %w", err)
		}
		appt = a
		return nil
	})
	if err != nil {
		return store.Appointment{}, mapSlotViolation(err)
	}
	return appt, nil
}
