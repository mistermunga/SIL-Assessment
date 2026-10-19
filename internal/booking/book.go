package booking

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"clinic/internal/apperr"
	"clinic/internal/auth"
	"clinic/internal/scheduling"
	"clinic/internal/store"
)

// Book reserves the slot starting at start for the calling patient.
func (s *Service) Book(ctx context.Context, p auth.Principal, doctorID int32, start time.Time) (store.Appointment, error) {
	if !p.IsPatient() {
		return store.Appointment{}, apperr.ErrForbidden
	}

	// Cheap checks first, before taking any lock.
	local := start.In(s.loc)
	if local.Second() != 0 || local.Nanosecond() != 0 {
		return store.Appointment{}, unavailable("start must be on the hour or half hour")
	}
	if !start.After(time.Now()) {
		return store.Appointment{}, unavailable("start is in the past")
	}

	var appt store.Appointment
	err := s.store.InTx(ctx, func(q *store.Queries) error {
		// Shared lock on the doctor row: bookings don't block each other, but a
		// schedule or leave edit (FOR UPDATE) waits for them, and they wait for it.
		if _, err := q.LockDoctorShared(ctx, doctorID); errors.Is(err, pgx.ErrNoRows) {
			return scheduling.ErrDoctorNotFound
		} else if err != nil {
			return fmt.Errorf("lock doctor: %w", err)
		}

		// Everything below reads state that can't change until we commit.
		shift, err := q.GetShift(ctx, store.GetShiftParams{
			DoctorID: doctorID,
			Weekday:  int16(scheduling.ISOWeekday(local)),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return unavailable("doctor does not work that day")
		}
		if err != nil {
			return fmt.Errorf("get shift: %w", err)
		}
		if !scheduling.WindowOf(shift).Fits(scheduling.MinutesOf(local)) {
			return unavailable("outside working hours or during the break")
		}

		onLeave, err := q.IsOnLeave(ctx, store.IsOnLeaveParams{DoctorID: doctorID, Day: dateOf(local)})
		if err != nil {
			return fmt.Errorf("check leave: %w", err)
		}
		if onLeave {
			return unavailable("doctor is on leave")
		}

		appt, err = q.InsertAppointment(ctx, store.InsertAppointmentParams{
			DoctorID:         doctorID,
			PatientID:        p.UserID,
			AppointmentStart: start,
		})
		if err != nil {
			return fmt.Errorf("insert appointment: %w", err)
		}
		return nil
	})
	if err != nil {
		if c, ok := store.UniqueViolation(err); ok {
			switch c {
			case constraintActiveSlot:
				return store.Appointment{}, ErrSlotTaken
			case constraintPatientSlot:
				return store.Appointment{}, ErrPatientBusy
			}
		}
		return store.Appointment{}, err
	}
	return appt, nil
}
