package httpapi

import (
	"context"
	"net/http"
	"time"
)

type Pinger interface {
	Ping(ctx context.Context) error
}

type HealthModule struct{ db Pinger }

func NewHealthModule(db Pinger) HealthModule { return HealthModule{db: db} }

func (m HealthModule) Routes() []Route {
	return []Route{
		{Pattern: "GET /healthz", Handler: m.check},
	}
}

func (m HealthModule) check(w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := m.db.Ping(ctx); err != nil {
		return &HTTPError{Status: http.StatusServiceUnavailable, Code: "unavailable",
			Message: "database unavailable", Err: err}
	}
	return writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
