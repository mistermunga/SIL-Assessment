package accounts

import (
	"context"
	"fmt"

	"clinic/internal/store"
)

type DoctorSummary struct {
	ID   int32
	Name string
}

// ListDoctors returns active doctors for patients to choose from.
// It exposes only the ID and display name, never credentials or contact details.
func (s *Service) ListDoctors(ctx context.Context) ([]DoctorSummary, error) {
	rows, err := s.store.ListDoctors(ctx)
	if err != nil {
		return nil, fmt.Errorf("list doctors: %w", err)
	}
	out := make([]DoctorSummary, len(rows))
	for i, r := range rows {
		out[i] = DoctorSummary{ID: r.DoctorID, Name: store.FullName(r.Firstname, r.Lastname)}
	}
	return out, nil
}
