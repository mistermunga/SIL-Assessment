package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type Role string

const (
	RolePatient Role = "patient"
	RoleDoctor  Role = "doctor"
	RoleAdmin   Role = "admin"
)

// Principal is the authenticated caller, passed from the HTTP layer down to services.
type Principal struct {
	UserID   int32
	Username string
	Role     Role
}

func (p Principal) IsPatient() bool { return p.Role == RolePatient }

// IsDoctor is true for admins too: an admin is a doctor with a flag.
func (p Principal) IsDoctor() bool { return p.Role == RoleDoctor || p.Role == RoleAdmin }
func (p Principal) IsAdmin() bool  { return p.Role == RoleAdmin }

// Authenticate turns a raw token into a Principal using the current database state,
// so a deleted or demoted user is locked out on the next request.
// Every rejection is ErrInvalidToken; any other error is an internal failure.
func (s *Service) Authenticate(ctx context.Context, rawToken string) (Principal, error) {
	id, err := s.tokens.Verify(rawToken)
	if err != nil {
		return Principal{}, ErrInvalidToken
	}

	row, err := s.store.GetPrincipal(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrInvalidToken // deleted, or never existed
	}
	if err != nil {
		return Principal{}, fmt.Errorf("get principal: %w", err)
	}

	role, ok := roleOf(row.IsPatient, row.IsDoctor, row.IsAdmin)
	if !ok {
		return Principal{}, ErrInvalidToken // user row with no role row
	}
	return Principal{UserID: row.UserID, Username: row.Username, Role: role}, nil
}

func roleOf(isPatient, isDoctor, isAdmin bool) (Role, bool) {
	switch {
	case isDoctor && isAdmin:
		return RoleAdmin, true
	case isDoctor:
		return RoleDoctor, true
	case isPatient:
		return RolePatient, true
	}
	return "", false
}
