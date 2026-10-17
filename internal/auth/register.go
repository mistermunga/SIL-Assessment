package auth

import (
	"clinic/internal/store"
	"context"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrUsernameTaken = errors.New("username taken")
	ErrEmailTaken    = errors.New("email taken")
)

// Postgres's default names for the column-level UNIQUE constraints in the migration.
const (
	constraintUsername = "users_username_key"
	constraintEmail    = "users_email_key"
)

type ValidationError struct {
	Problems map[string]string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid input: %v", e.Problems)
}

type RegisterInput struct {
	Username  string
	Email     string
	Password  string
	Firstname string
	Lastname  string
}

type DoctorInput struct {
	UserInfo RegisterInput
	IsAdmin  bool
}

type Service struct {
	store *store.Store
}

func NewService(s *store.Store) *Service {
	return &Service{s}
}

var usernameRe = regexp.MustCompile(`^[a-z0-9_]{3,20}$`)

func (s *Service) register(ctx context.Context, input RegisterInput, addRole func(q *store.Queries, userID int32) error) (store.User, error) {
	input.Username = strings.ToLower(strings.TrimSpace(input.Username))
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	input.Firstname = strings.TrimSpace(input.Firstname)
	input.Lastname = strings.TrimSpace(input.Lastname)

	if err := validateRegistration(input); err != nil {
		return store.User{}, err
	}

	hash, err := HashPassword(input.Password)
	if err != nil {
		return store.User{}, fmt.Errorf("hash password: %w", err)
	}

	var user store.User
	err = s.store.InTx(ctx, func(q *store.Queries) error {
		u, err := q.CreateUser(ctx, store.CreateUserParams{
			Username:     input.Username,
			Email:        input.Email,
			PasswordHash: hash,
			Firstname:    optText(input.Firstname),
			Lastname:     optText(input.Lastname),
		})
		if err != nil {
			return err
		}
		user = u
		return addRole(q, u.UserID)
	})
	if err != nil {
		if c, ok := store.UniqueViolation(err); ok {
			switch c {
			case constraintUsername:
				return store.User{}, ErrUsernameTaken
			case constraintEmail:
				return store.User{}, ErrEmailTaken
			}
		}
		return store.User{}, fmt.Errorf("register user: %w", err)
	}
	return user, nil
}

func (s *Service) RegisterPatient(ctx context.Context, in RegisterInput) (store.User, error) {
	return s.register(ctx, in, func(q *store.Queries, id int32) error {
		return q.CreatePatient(ctx, id)
	})
}

func (s *Service) RegisterDoctor(ctx context.Context, in DoctorInput) (store.User, error) {
	return s.register(ctx, in.UserInfo, func(q *store.Queries, userID int32) error {
		return q.CreateDoctor(ctx, store.CreateDoctorParams{DoctorID: userID, IsAdmin: in.IsAdmin})
	})
}

func validateRegistration(input RegisterInput) error {
	p := map[string]string{}

	if !usernameRe.MatchString(input.Username) {
		p["username"] = "3-20 characters: letters, digits, underscore"
	}
	if addr, err := mail.ParseAddress(input.Email); err != nil || addr.Address != input.Email || len(input.Email) > 50 {
		p["email"] = "must be a valid email of at most 50 characters"
	}

	switch {
	case len(input.Password) < 8:
		p["password"] = "must be at least 8 characters"
	case len(input.Password) > 72:
		p["password"] = "must be at most 72 characters"
	}
	if len(input.Firstname) > 100 {
		p["firstname"] = "must be at most 100 characters"
	}
	if len(input.Lastname) > 100 {
		p["lastname"] = "must be at most 100 characters"
	}

	if len(p) > 0 {
		return &ValidationError{p}
	}

	return nil
}

func optText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}
