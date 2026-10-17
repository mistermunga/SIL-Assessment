package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

var ErrInvalidCredentials = errors.New("invalid credentials")

// dummyHash is compared against when the username doesn't exist, so a failed
// login takes about as long whether the account exists or not.
var dummyHash = mustHash("timing-equaliser")

func mustHash(p string) []byte {
	b, err := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	if err != nil {
		panic(err) // only possible for an invalid cost or oversized input; neither applies
	}
	return b
}

type LoginResult struct {
	Token     string
	ExpiresAt time.Time
}

func (s *Service) Login(ctx context.Context, username, password string) (LoginResult, error) {
	username = strings.ToLower(strings.TrimSpace(username))

	hash := dummyHash
	found := false

	u, err := s.store.GetUserByUsername(ctx, username)
	switch {
	case err == nil:
		hash, found = []byte(u.PasswordHash), true
	case errors.Is(err, pgx.ErrNoRows):
		// fall through with the dummy hash
	default:
		return LoginResult{}, fmt.Errorf("get user: %w", err)
	}

	// Always compare, even for unknown users.
	passwordOK := bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
	if !found || !passwordOK {
		return LoginResult{}, ErrInvalidCredentials
	}

	token, exp, err := s.tokens.Issue(u.UserID)
	if err != nil {
		return LoginResult{}, fmt.Errorf("issue token: %w", err)
	}
	return LoginResult{Token: token, ExpiresAt: exp}, nil
}
