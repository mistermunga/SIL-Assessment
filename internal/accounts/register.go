package accounts

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"clinic/internal/apperr"
	"clinic/internal/password"
	"clinic/internal/store"
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

var usernameRe = regexp.MustCompile(`^[a-z0-9_]{3,20}$`)

func (s *Service) RegisterPatient(ctx context.Context, in RegisterInput) (store.User, error) {
	return s.register(ctx, in, func(q *store.Queries, id int32) error {
		return q.CreatePatient(ctx, id)
	})
}

func (s *Service) RegisterDoctor(ctx context.Context, in DoctorInput) (store.User, error) {
	return s.register(ctx, in.UserInfo, func(q *store.Queries, id int32) error {
		return q.CreateDoctor(ctx, store.CreateDoctorParams{DoctorID: id, IsAdmin: in.IsAdmin})
	})
}

func (s *Service) register(ctx context.Context, in RegisterInput,
	addRole func(q *store.Queries, userID int32) error) (store.User, error) {

	in.Username = strings.ToLower(strings.TrimSpace(in.Username))
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.Firstname = strings.TrimSpace(in.Firstname)
	in.Lastname = strings.TrimSpace(in.Lastname)

	if err := validateRegistration(in); err != nil {
		return store.User{}, err
	}

	hash, err := password.Hash(in.Password)
	if err != nil {
		return store.User{}, fmt.Errorf("hash password: %w", err)
	}

	var user store.User
	err = s.store.InTx(ctx, func(q *store.Queries) error {
		u, err := q.CreateUser(ctx, store.CreateUserParams{
			Username:     in.Username,
			Email:        in.Email,
			PasswordHash: hash,
			Firstname:    optText(in.Firstname),
			Lastname:     optText(in.Lastname),
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

func validateRegistration(in RegisterInput) error {
	p := map[string]string{}

	if !usernameRe.MatchString(in.Username) {
		p["username"] = "3-20 characters: letters, digits, underscore"
	}
	if addr, err := mail.ParseAddress(in.Email); err != nil || addr.Address != in.Email || len(in.Email) > 50 {
		p["email"] = "must be a valid email of at most 50 characters"
	}
	switch {
	case len(in.Password) < 8:
		p["password"] = "must be at least 8 characters"
	case len(in.Password) > password.MaxBytes:
		p["password"] = "must be at most 72 bytes"
	}
	if len(in.Firstname) > 100 {
		p["firstname"] = "must be at most 100 characters"
	}
	if len(in.Lastname) > 100 {
		p["lastname"] = "must be at most 100 characters"
	}

	if len(p) > 0 {
		return &apperr.ValidationError{Problems: p}
	}
	return nil
}

func optText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}
