package scheduling

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"clinic/internal/apperr"
	"clinic/internal/store"
)

var (
	ErrDoctorNotFound          = errors.New("doctor not found")
	ErrShiftNotFound           = errors.New("no shift on that weekday")
	ErrConflictingAppointments = errors.New("booked appointments fall outside the new hours")
)

type Service struct {
	store *store.Store
	loc   *time.Location // clinic time zone: decides which weekday and clock time an appointment falls on
}

func NewService(s *store.Store, loc *time.Location) *Service {
	return &Service{store: s, loc: loc}
}

// SetShift creates or replaces a doctor's shift for one weekday.
func (s *Service) SetShift(ctx context.Context, doctorID int32, in Shift) (Shift, error) {
	w, err := parseShift(in)
	if err != nil {
		return Shift{}, err
	}

	var saved store.DoctorSchedule
	err = s.store.InTx(ctx, func(q *store.Queries) error {
		if err := lockDoctor(ctx, q, doctorID); err != nil {
			return err
		}
		if err := s.checkNoConflicts(ctx, q, doctorID, in.Weekday, &w); err != nil {
			return err
		}

		params := store.UpsertShiftParams{
			DoctorID:  doctorID,
			Weekday:   int16(in.Weekday),
			StartTime: pgTime(w.Start),
			EndTime:   pgTime(w.End),
		}
		if w.HasBreak {
			params.BreakStart, params.BreakEnd = pgTime(w.BreakStart), pgTime(w.BreakEnd)
		}

		row, err := q.UpsertShift(ctx, params)
		if err != nil {
			return fmt.Errorf("upsert shift: %w", err)
		}
		saved = row
		return nil
	})
	if err != nil {
		return Shift{}, err
	}
	return toShift(saved), nil
}

// ClearShift removes a doctor's working day. Rejected while future booked appointments exist on it.
func (s *Service) ClearShift(ctx context.Context, doctorID int32, weekday int) error {
	if weekday < 1 || weekday > 5 {
		return &apperr.ValidationError{Problems: map[string]string{"weekday": "must be 1 (Monday) to 5 (Friday)"}}
	}

	return s.store.InTx(ctx, func(q *store.Queries) error {
		if err := lockDoctor(ctx, q, doctorID); err != nil {
			return err
		}
		if err := s.checkNoConflicts(ctx, q, doctorID, weekday, nil); err != nil {
			return err
		}
		n, err := q.DeleteShift(ctx, store.DeleteShiftParams{DoctorID: doctorID, Weekday: int16(weekday)})
		if err != nil {
			return fmt.Errorf("delete shift: %w", err)
		}
		if n == 0 {
			return ErrShiftNotFound
		}
		return nil
	})
}

// GetSchedule returns a doctor's shifts ordered by weekday.
func (s *Service) GetSchedule(ctx context.Context, doctorID int32) ([]Shift, error) {
	rows, err := s.store.ListShifts(ctx, doctorID)
	if err != nil {
		return nil, fmt.Errorf("list shifts: %w", err)
	}
	out := make([]Shift, len(rows))
	for i, r := range rows {
		out[i] = toShift(r)
	}
	return out, nil
}

// checkNoConflicts fails if any future booked appointment on that weekday would not
// fit the proposed window. A nil window means the doctor stops working that day.
func (s *Service) checkNoConflicts(ctx context.Context, q *store.Queries, doctorID int32, weekday int, w *Window) error {
	starts, err := q.ListFutureBookedStarts(ctx, doctorID)
	if err != nil {
		return fmt.Errorf("list booked starts: %w", err)
	}
	for _, t := range starts {
		local := t.In(s.loc)
		if ISOWeekday(local) != weekday {
			continue
		}
		if w == nil || !w.Fits(MinutesOf(local)) {
			return ErrConflictingAppointments
		}
	}
	return nil
}

func lockDoctor(ctx context.Context, q *store.Queries, doctorID int32) error {
	_, err := q.LockDoctor(ctx, doctorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrDoctorNotFound
	}
	if err != nil {
		return fmt.Errorf("lock doctor: %w", err)
	}
	return nil
}
