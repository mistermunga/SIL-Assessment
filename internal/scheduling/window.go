package scheduling

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"clinic/internal/store"
)

const (
	SlotMinutes     = 30
	microsPerMinute = int64(60 * 1_000_000)
)

// Window is one working day in minutes since midnight, in clinic local time.
// It is the single definition of which slots are bookable.
type Window struct {
	Start, End           int
	HasBreak             bool
	BreakStart, BreakEnd int
}

// Fits reports whether a slot starting at minute m is bookable:
// inside the shift and not overlapping the break.
func (w Window) Fits(m int) bool {
	if m%SlotMinutes != 0 || m < w.Start || m+SlotMinutes > w.End {
		return false
	}
	if w.HasBreak && m < w.BreakEnd && m+SlotMinutes > w.BreakStart {
		return false
	}
	return true
}

// Slots lists every bookable slot start in order.
func (w Window) Slots() []int {
	var out []int
	for m := w.Start; m+SlotMinutes <= w.End; m += SlotMinutes {
		if w.Fits(m) {
			out = append(out, m)
		}
	}
	return out
}

// WindowOf builds a Window from a stored schedule row.
func WindowOf(r store.DoctorSchedule) Window {
	w := Window{Start: minutesOf(r.StartTime), End: minutesOf(r.EndTime)}
	if r.BreakStart.Valid {
		w.HasBreak = true
		w.BreakStart, w.BreakEnd = minutesOf(r.BreakStart), minutesOf(r.BreakEnd)
	}
	return w
}

// MinutesOf returns the clock time of t, in t's own location, as minutes since midnight.
func MinutesOf(t time.Time) int { return t.Hour()*60 + t.Minute() }

// ISOWeekday maps Go's Sunday=0 to ISO's Monday=1 ... Sunday=7.
func ISOWeekday(t time.Time) int {
	if wd := int(t.Weekday()); wd != 0 {
		return wd
	}
	return 7
}

func minutesOf(t pgtype.Time) int { return int(t.Microseconds / microsPerMinute) }
