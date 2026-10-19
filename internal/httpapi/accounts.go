package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"clinic/internal/accounts"
	"clinic/internal/auth"
	"clinic/internal/store"
)

// Registrar is what this module needs from the accounts service.
type Registrar interface {
	RegisterPatient(ctx context.Context, in accounts.RegisterInput) (store.User, error)
}

// TokenIssuer signs a token for a user. *auth.TokenManager satisfies it.
type TokenIssuer interface {
	Issue(userID int32) (token string, expiresAt time.Time, err error)
}

type AccountsModule struct {
	svc    Registrar
	tokens TokenIssuer
}

func NewAccountsModule(svc Registrar, tokens TokenIssuer) AccountsModule {
	return AccountsModule{svc: svc, tokens: tokens}
}

func (m AccountsModule) Routes() []Route {
	return []Route{
		{Pattern: "POST /patients", Handler: m.registerPatient}, // no Access: open
	}
}

func (AccountsModule) ErrorRules() []ErrorRule {
	return []ErrorRule{
		{Is: accounts.ErrUsernameTaken, Status: http.StatusConflict, Code: "username_taken"},
		{Is: accounts.ErrEmailTaken, Status: http.StatusConflict, Code: "email_taken"},
	}
}

// registerRequest lists exactly the fields a client may set. Unknown fields are
// rejected by readJSON, so a request can't smuggle in a role or an admin flag.
type registerRequest struct {
	Username  string `json:"username"`
	Email     string `json:"email"`
	Password  string `json:"password"`
	Firstname string `json:"firstname"`
	Lastname  string `json:"lastname"`
}

// userResponse is the only shape in which a user leaves the API.
type userResponse struct {
	ID        int32     `json:"id"`
	Username  string    `json:"username"`
	Email     string    `json:"email"`
	Firstname string    `json:"firstname"`
	Lastname  string    `json:"lastname"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

func newUserResponse(u store.User, role auth.Role) userResponse {
	return userResponse{
		ID:        u.UserID,
		Username:  u.Username,
		Email:     u.Email,
		Firstname: u.Firstname.String,
		Lastname:  u.Lastname.String,
		Role:      string(role),
		CreatedAt: u.CreatedAt,
	}
}

// registrationResponse is the user plus a ready-to-use token. The embedded
// tokenResponse's fields appear at the top level of the JSON.
type registrationResponse struct {
	User userResponse `json:"user"`
	tokenResponse
}

func (m AccountsModule) registerPatient(w http.ResponseWriter, r *http.Request) error {
	var req registerRequest
	if err := readJSON(w, r, &req); err != nil {
		return err
	}

	u, err := m.svc.RegisterPatient(r.Context(), accounts.RegisterInput{
		Username:  req.Username,
		Email:     req.Email,
		Password:  req.Password,
		Firstname: req.Firstname,
		Lastname:  req.Lastname,
	})
	if err != nil {
		return err // the server maps it: 409, 422 or a generic 500
	}

	token, expiresAt, err := m.tokens.Issue(u.UserID)
	if err != nil {
		return fmt.Errorf("issue token for new user %d: %w", u.UserID, err)
	}
	return writeJSON(w, http.StatusCreated, registrationResponse{
		User:          newUserResponse(u, auth.RolePatient),
		tokenResponse: newTokenResponse(token, expiresAt),
	})
}
