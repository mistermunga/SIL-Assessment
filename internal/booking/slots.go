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

// FreeSlots lists the doctor's open slot starts on a calendar day, in clinic time.
// Only the year, month and day of day are used. The result is a snapshot:
// Book remains the authority on whether a slot is still free.
func (s *Service) FreeSlots(ctx context.Context, doctorID int32, day time.Time) ([]time.Time, error) {
	y, mo, d := day.Date()
	dayStart := time.Date(y, mo, d, 0, 0, 0, 0, s.loc)
	dayEnd := time.Date(y, mo, d+1, 0, 0, 0, 0, s.loc)

	exists, err := s.store.DoctorExists(ctx, doctorID)
	if err != nil {
		return nil, fmt.Errorf("check doctor: %w", err)
	}
	if !exists {
		return nil, scheduling.ErrDoctorNotFound
	}

	shift, err := s.store.GetShift(ctx, store.GetShiftParams{
		DoctorID: doctorID,
		Weekday:  int16(scheduling.ISOWeekday(dayStart)),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return []time.Time{}, nil // not a working day
	}
	if err != nil {
		return nil, fmt.Errorf("get shift: %w", err)
	}

	onLeave, err := s.store.IsOnLeave(ctx, store.IsOnLeaveParams{DoctorID: doctorID, Day: dateOf(dayStart)})
	if err != nil {
		return nil, fmt.Errorf("check leave: %w", err)
	}
	if onLeave {
		return []time.Time{}, nil
	}

	booked, err := s.store.ListBookedStarts(ctx, store.ListBookedStartsParams{
		DoctorID: doctorID, FromTime: dayStart, ToTime: dayEnd,
	})
	if err != nil {
		return nil, fmt.Errorf("list booked: %w", err)
	}
	taken := make(map[int64]struct{}, len(booked))
	for _, b := range booked {
		taken[b.Unix()] = struct{}{}
	}

	now := time.Now()
	out := []time.Time{}
	for _, m := range scheduling.WindowOf(shift).Slots() {
		slot := time.Date(y, mo, d, m/60, m%60, 0, 0, s.loc)
		if !slot.After(now) {
			continue
		}
		if _, ok := taken[slot.Unix()]; ok {
			continue
		}
		out = append(out, slot)
	}
	return out, nil
}
