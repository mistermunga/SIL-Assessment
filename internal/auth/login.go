package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"clinic/internal/password"
)

var ErrInvalidCredentials = errors.New("invalid credentials")

type LoginResult struct {
	Token     string
	ExpiresAt time.Time
}

func (s *Service) Login(ctx context.Context, username, plain string) (LoginResult, error) {
	username = strings.ToLower(strings.TrimSpace(username))

	u, err := s.store.GetUserByUsername(ctx, username) // excludes soft-deleted users
	if errors.Is(err, pgx.ErrNoRows) {
		password.SpendTime(plain)
		return LoginResult{}, ErrInvalidCredentials
	}
	if err != nil {
		return LoginResult{}, fmt.Errorf("get user: %w", err)
	}
	if !password.Matches(u.PasswordHash, plain) {
		return LoginResult{}, ErrInvalidCredentials
	}

	token, exp, err := s.tokens.Issue(u.UserID)
	if err != nil {
		return LoginResult{}, fmt.Errorf("issue token: %w", err)
	}
	return LoginResult{Token: token, ExpiresAt: exp}, nil
}
