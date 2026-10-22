package booking

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"clinic/internal/scheduling"
	"clinic/internal/store"
)

// minLeadTime is the notice required before an appointment starts.
const minLeadTime = time.Hour

// checkStart runs the checks that need no database: grid alignment, not in the past,
// and at least minLeadTime from now.
func (s *Service) checkStart(start time.Time) error {
	local := start.In(s.loc)
	if local.Second() != 0 || local.Nanosecond() != 0 {
		return unavailable("start must be on the hour or half hour")
	}
	now := time.Now()
	if !start.After(now) {
		return unavailable("start is in the past")
	}
	if start.Before(now.Add(minLeadTime)) {
		return unavailable("start must be at least 1 hour from now")
	}
	return nil
}

// lockDoctorShared takes a shared lock on the doctor row. Bookings and reschedules don't
// block each other, but schedule and leave edits (FOR UPDATE) wait for them and vice versa.
func lockDoctorShared(ctx context.Context, q *store.Queries, doctorID int32) error {
	_, err := q.LockDoctorShared(ctx, doctorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return scheduling.ErrDoctorNotFound
	}
	if err != nil {
		return fmt.Errorf("lock doctor: %w", err)
	}
	return nil
}

// checkSlot validates a start time against the doctor's shift, break and leave.
// The caller must hold the doctor lock so none of this can change before commit.
func (s *Service) checkSlot(ctx context.Context, q *store.Queries, doctorID int32, start time.Time) error {
	local := start.In(s.loc)

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
	return nil
}

// mapSlotViolation turns the two double-booking indexes into domain errors.
func mapSlotViolation(err error) error {
	if c, ok := store.UniqueViolation(err); ok {
		switch c {
		case constraintActiveSlot:
			return ErrSlotTaken
		case constraintPatientSlot:
			return ErrPatientBusy
		}
	}
	return err
}
