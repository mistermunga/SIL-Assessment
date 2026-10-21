package httpapi

import (
	"context"
	"net/http"

	"clinic/internal/accounts"
	"clinic/internal/auth"
	"clinic/internal/store"
)

// DoctorService is what this module needs from the accounts service.
type DoctorService interface {
	ListDoctors(ctx context.Context) ([]accounts.DoctorSummary, error)
	RegisterDoctor(ctx context.Context, in accounts.DoctorInput) (store.User, error)
}

var _ DoctorService = (*accounts.Service)(nil)

type DoctorsModule struct{ svc DoctorService }

func NewDoctorsModule(svc DoctorService) DoctorsModule { return DoctorsModule{svc: svc} }

func (m DoctorsModule) Routes() []Route {
	return []Route{
		{Pattern: "GET /doctors", Access: Authenticated, Handler: m.list},
		{Pattern: "POST /doctors", Access: Admin, Handler: m.create},
	}
}

func (DoctorsModule) ErrorRules() []ErrorRule {
	return []ErrorRule{
		{Is: accounts.ErrUsernameTaken, Status: http.StatusConflict, Code: "username_taken"},
		{Is: accounts.ErrEmailTaken, Status: http.StatusConflict, Code: "email_taken"},
	}
}

type doctorResponse struct {
	ID   int32  `json:"id"`
	Name string `json:"name"`
}

func (m DoctorsModule) list(w http.ResponseWriter, r *http.Request) error {
	list, err := m.svc.ListDoctors(r.Context())
	if err != nil {
		return err
	}
	out := make([]doctorResponse, len(list))
	for i, d := range list {
		out[i] = doctorResponse{ID: d.ID, Name: d.Name}
	}
	return writeJSON(w, http.StatusOK, map[string]any{"doctors": out})
}

type createDoctorRequest struct {
	Username  string `json:"username"`
	Email     string `json:"email"`
	Password  string `json:"password"`
	Firstname string `json:"firstname"`
	Lastname  string `json:"lastname"`
	IsAdmin   bool   `json:"is_admin"`
}

func (m DoctorsModule) create(w http.ResponseWriter, r *http.Request) error {
	var req createDoctorRequest
	if err := readJSON(w, r, &req); err != nil {
		return err
	}

	u, err := m.svc.RegisterDoctor(r.Context(), accounts.DoctorInput{
		UserInfo: accounts.RegisterInput{
			Username:  req.Username,
			Email:     req.Email,
			Password:  req.Password,
			Firstname: req.Firstname,
			Lastname:  req.Lastname,
		},
		IsAdmin: req.IsAdmin,
	})
	if err != nil {
		return err
	}

	role := auth.RoleDoctor
	if req.IsAdmin {
		role = auth.RoleAdmin
	}
	return writeJSON(w, http.StatusCreated, newUserResponse(u, role))
}
