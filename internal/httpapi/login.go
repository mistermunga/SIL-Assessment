package httpapi

import (
	"context"
	"net/http"

	"clinic/internal/auth"
)

type LoginService interface {
	Login(ctx context.Context, username, plain string) (auth.LoginResult, error)
}

type AuthModule struct{ svc LoginService }

func NewAuthModule(svc LoginService) AuthModule { return AuthModule{svc: svc} }

func (m AuthModule) Routes() []Route {
	return []Route{
		{Pattern: "POST /login", Handler: m.login}, // no Access: open
	}
}

func (AuthModule) ErrorRules() []ErrorRule {
	return []ErrorRule{
		{Is: auth.ErrInvalidCredentials, Status: http.StatusUnauthorized, Code: "invalid_credentials"},
	}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (m AuthModule) login(w http.ResponseWriter, r *http.Request) error {
	var req loginRequest
	if err := readJSON(w, r, &req); err != nil {
		return err
	}

	res, err := m.svc.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		return err // the server maps it: 401 or a generic 500
	}
	return writeJSON(w, http.StatusOK, newTokenResponse(res.Token, res.ExpiresAt))
}
