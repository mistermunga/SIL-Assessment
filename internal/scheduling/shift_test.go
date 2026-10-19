package scheduling

import (
	"errors"
	"testing"

	"clinic/internal/apperr"
)

func TestParseShift(t *testing.T) {
	ok := Shift{Weekday: 1, Start: "09:00", End: "17:00", BreakStart: "12:00", BreakEnd: "13:00"}

	with := func(f func(*Shift)) Shift { s := ok; f(&s); return s }

	cases := []struct {
		name      string
		in        Shift
		wantField string // "" means valid
	}{
		{"valid with break", ok, ""},
		{"valid without break", with(func(s *Shift) { s.BreakStart, s.BreakEnd = "", "" }), ""},
		{"saturday", with(func(s *Shift) { s.Weekday = 6 }), "weekday"},
		{"off the half hour", with(func(s *Shift) { s.Start = "09:15" }), "start"},
		{"not a time", with(func(s *Shift) { s.End = "5pm" }), "end"},
		{"end before start", with(func(s *Shift) { s.End = "08:00" }), "end"},
		{"half a break", with(func(s *Shift) { s.BreakEnd = "" }), "break"},
		{"break outside shift", with(func(s *Shift) { s.BreakStart, s.BreakEnd = "16:30", "17:30" }), "break"},
		{"break over an hour", with(func(s *Shift) { s.BreakStart, s.BreakEnd = "12:00", "13:30" }), "break"},
		{"break backwards", with(func(s *Shift) { s.BreakStart, s.BreakEnd = "13:00", "12:00" }), "break_end"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseShift(c.in)
			if c.wantField == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var ve *apperr.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want ValidationError", err)
			}
			if _, ok := ve.Problems[c.wantField]; !ok {
				t.Errorf("problems = %v, want a message for %q", ve.Problems, c.wantField)
			}
		})
	}
}
