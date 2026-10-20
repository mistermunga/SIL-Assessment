package accounts

import (
	"context"
	"errors"
	"fmt"
)

// EnsureAdmin creates the initial administrator when no active one exists.
// It reports whether it created one. Safe to call on every startup, including
// from several instances at once.
func (s *Service) EnsureAdmin(ctx context.Context, in RegisterInput) (bool, error) {
	exists, err := s.store.AdminExists(ctx)
	if err != nil {
		return false, fmt.Errorf("check for administrator: %w", err)
	}
	if exists {
		return false, nil
	}

	_, err = s.RegisterDoctor(ctx, DoctorInput{UserInfo: in, IsAdmin: true})
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrUsernameTaken), errors.Is(err, ErrEmailTaken):
		// Either another instance just created the admin, or someone else holds the name.
		exists, checkErr := s.store.AdminExists(ctx)
		if checkErr != nil {
			return false, fmt.Errorf("check for administrator: %w", checkErr)
		}
		if exists {
			return false, nil
		}
		return false, fmt.Errorf("cannot create the initial administrator (change ADMIN_USERNAME or ADMIN_EMAIL): %w", err)
	default:
		return false, err
	}
}
