package scheduling

import (
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"clinic/internal/apperr"
	"clinic/internal/store"
)

const (
	maxBreakMinutes = 60
	clockHint       = "must be HH:MM on the hour or half hour"
)

// Shift is one weekday's working hours, used for both input and output.
// Times are "HH:MM"; BreakStart and BreakEnd are "" when there is no break.
type Shift struct {
	Weekday    int // 1 = Monday ... 5 = Friday
	Start      string
	End        string
	BreakStart string
	BreakEnd   string
}

func parseShift(in Shift) (Window, error) {
	p := map[string]string{}
	var w Window

	if in.Weekday < 1 || in.Weekday > 5 {
		p["weekday"] = "must be 1 (Monday) to 5 (Friday)"
	}

	start, okStart := parseClock(in.Start)
	if !okStart {
		p["start"] = clockHint
	}
	end, okEnd := parseClock(in.End)
	if !okEnd {
		p["end"] = clockHint
	}
	if okStart && okEnd && start >= end {
		p["end"] = "must be after start"
	}
	w.Start, w.End = start, end

	switch {
	case in.BreakStart == "" && in.BreakEnd == "":
		// no break
	case in.BreakStart == "" || in.BreakEnd == "":
		p["break"] = "give both break_start and break_end, or neither"
	default:
		bs, okBS := parseClock(in.BreakStart)
		be, okBE := parseClock(in.BreakEnd)
		if !okBS {
			p["break_start"] = clockHint
		}
		if !okBE {
			p["break_end"] = clockHint
		}
		if okBS && okBE {
			switch {
			case bs >= be:
				p["break_end"] = "must be after break_start"
			case okStart && okEnd && (bs < start || be > end):
				p["break"] = "must fall inside the shift"
			case be-bs > maxBreakMinutes:
				p["break"] = "must be at most 60 minutes"
			}
			w.HasBreak, w.BreakStart, w.BreakEnd = true, bs, be
		}
	}

	if len(p) > 0 {
		return Window{}, &apperr.ValidationError{Problems: p}
	}
	return w, nil
}

// parseClock parses "HH:MM" into minutes since midnight and requires a 30-minute boundary.
func parseClock(v string) (int, bool) {
	t, err := time.Parse("15:04", v)
	if err != nil {
		return 0, false
	}
	m := MinutesOf(t)
	return m, m%SlotMinutes == 0
}

func pgTime(minutes int) pgtype.Time {
	return pgtype.Time{Microseconds: int64(minutes) * microsPerMinute, Valid: true}
}

func clockString(t pgtype.Time) string {
	m := minutesOf(t)
	return fmt.Sprintf("%02d:%02d", m/60, m%60)
}

func toShift(r store.DoctorSchedule) Shift {
	s := Shift{Weekday: int(r.Weekday), Start: clockString(r.StartTime), End: clockString(r.EndTime)}
	if r.BreakStart.Valid {
		s.BreakStart, s.BreakEnd = clockString(r.BreakStart), clockString(r.BreakEnd)
	}
	return s
}
