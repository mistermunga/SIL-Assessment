package booking

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"clinic/internal/apperr"
	"clinic/internal/auth"
	"clinic/internal/store"
)

// Reschedule moves the calling patient's own future booked appointment to a new start
// with the same doctor. The new slot is validated exactly as for a fresh booking.
// The row is updated in place in one transaction: the old slot frees at commit, and a
// failure leaves the original appointment untouched.
func (s *Service) Reschedule(ctx context.Context, p auth.Principal, appointmentID int32, newStart time.Time) (store.Appointment, error) {
	if !p.IsPatient() {
		return store.Appointment{}, apperr.ErrForbidden
	}
	if err := s.checkStart(newStart); err != nil {
		return store.Appointment{}, err
	}

	var moved store.Appointment
	err := s.store.InTx(ctx, func(q *store.Queries) error {
		lookup := store.GetPatientAppointmentParams{AppointmentID: appointmentID, PatientID: p.UserID}

		// 1. Find the doctor with an unlocked read (an appointment's doctor never changes).
		appt, err := q.GetPatientAppointment(ctx, lookup)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound // unknown id or someone else's: indistinguishable on purpose
		}
		if err != nil {
			return fmt.Errorf("get appointment: %w", err)
		}

		// 2. Same lock order as Book: doctor row first.
		if err := lockDoctorShared(ctx, q, appt.DoctorID); err != nil {
			return err
		}

		// 3. Re-read under a row lock: a concurrent cancel or reschedule has finished by now.
		appt, err = q.GetPatientAppointmentForUpdate(ctx, store.GetPatientAppointmentForUpdateParams(lookup))
		if err != nil {
			return fmt.Errorf("lock appointment: %w", err)
		}
		switch {
		case appt.Status == store.AppointmentStatusCancelled:
			return ErrAlreadyCancelled
		case appt.Status != store.AppointmentStatusBooked:
			return fmt.Errorf("%w: appointment is %s", ErrNotReschedulable, appt.Status)
		case !appt.AppointmentStart.After(time.Now()):
			return fmt.Errorf("%w: already started", ErrNotReschedulable)
		case appt.AppointmentStart.Equal(newStart):
			return &apperr.ValidationError{Problems: map[string]string{"start": "is the current appointment time"}}
		}

		// 4. Validate the new slot as for a fresh booking.
		if err := s.checkSlot(ctx, q, appt.DoctorID, newStart); err != nil {
			return err
		}

		// 5. Move it. The unique indexes arbitrate against concurrent bookings.
		moved, err = q.RescheduleAppointment(ctx, store.RescheduleAppointmentParams{
			AppointmentID:    appointmentID,
			AppointmentStart: newStart,
		})
		if err != nil {
			return fmt.Errorf("reschedule: %w", err)
		}
		return nil
	})
	if err != nil {
		return store.Appointment{}, mapSlotViolation(err)
	}
	return moved, nil
}
