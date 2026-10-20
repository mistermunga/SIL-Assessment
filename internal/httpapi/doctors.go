package httpapi

import (
	"context"
	"net/http"

	"clinic/internal/accounts"
)

type DoctorLister interface {
	ListDoctors(ctx context.Context) ([]accounts.DoctorSummary, error)
}

var _ DoctorLister = (*accounts.Service)(nil)

type DoctorsModule struct{ svc DoctorLister }

func NewDoctorsModule(svc DoctorLister) DoctorsModule { return DoctorsModule{svc: svc} }

func (m DoctorsModule) Routes() []Route {
	return []Route{
		{Pattern: "GET /doctors", Access: Authenticated, Handler: m.list},
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
