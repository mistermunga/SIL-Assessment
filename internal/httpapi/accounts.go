package httpapi

import (
	"clinic/internal/accounts"
	"clinic/internal/auth"
	"clinic/internal/store"
	"context"
	"net/http"
	"time"
)

type Registrar interface {
	RegisterPatient(ctx context.Context, input accounts.RegisterInput) (store.User, error)
}

type AccountsModule struct {
	svc Registrar
}

func (m AccountsModule) Routes() []Route {
	return []Route{
		{Pattern: "POST /patients", Handler: m.registerPatient},
	}
}

func NewAccountsModule(svc Registrar) AccountsModule {
	return AccountsModule{svc: svc}
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
	return writeJSON(w, http.StatusCreated, newUserResponse(u, auth.RolePatient))
}
