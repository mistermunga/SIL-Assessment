package scheduling

import (
	"slices"
	"testing"
)

var lunchDay = Window{Start: 9 * 60, End: 17 * 60, HasBreak: true, BreakStart: 12 * 60, BreakEnd: 13 * 60}

func TestWindowFits(t *testing.T) {
	cases := []struct {
		clock string
		min   int
		want  bool
	}{
		{"08:30", 8*60 + 30, false},
		{"09:00", 9 * 60, true},
		{"11:30", 11*60 + 30, true}, // ends exactly when the break starts
		{"12:00", 12 * 60, false},
		{"12:30", 12*60 + 30, false},
		{"13:00", 13 * 60, true}, // starts exactly when the break ends
		{"16:30", 16*60 + 30, true},
		{"17:00", 17 * 60, false},
		{"09:15", 9*60 + 15, false},
	}
	for _, c := range cases {
		if got := lunchDay.Fits(c.min); got != c.want {
			t.Errorf("Fits(%s) = %v, want %v", c.clock, got, c.want)
		}
	}
}

func TestWindowSlots(t *testing.T) {
	got := lunchDay.Slots()
	if len(got) != 14 { // 16 half-hours in 8h, minus the 2 in the break
		t.Fatalf("len = %d, want 14", len(got))
	}
	if got[0] != 9*60 || got[len(got)-1] != 16*60+30 {
		t.Errorf("first/last = %d/%d", got[0], got[len(got)-1])
	}
	for _, m := range []int{12 * 60, 12*60 + 30} {
		if slices.Contains(got, m) {
			t.Errorf("slot %d is inside the break", m)
		}
	}
}
