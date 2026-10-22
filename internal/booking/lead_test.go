package booking

import (
	"errors"
	"testing"
	"time"
)

func TestCheckStartLeadTime(t *testing.T) {
	s := &Service{loc: time.UTC}
	base := time.Now().Truncate(time.Minute)

	cases := []struct {
		name  string
		start time.Time
		ok    bool
	}{
		{"in the past", base.Add(-30 * time.Minute), false},
		{"in 10 minutes", base.Add(10 * time.Minute), false},
		{"in 59 minutes", base.Add(59 * time.Minute), false},
		{"in 61 minutes", base.Add(61 * time.Minute), true},
		{"tomorrow", base.Add(24 * time.Hour), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := s.checkStart(c.start)
			switch {
			case c.ok && err != nil:
				t.Fatalf("err = %v, want nil", err)
			case !c.ok && !errors.Is(err, ErrSlotUnavailable):
				t.Fatalf("err = %v, want ErrSlotUnavailable", err)
			}
		})
	}
}
