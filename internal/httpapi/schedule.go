package httpapi

import (
	"context"
	"net/http"
	"strconv"

	"clinic/internal/apperr"
	"clinic/internal/auth"
	"clinic/internal/scheduling"
)

// ScheduleService is what this module needs from the scheduling service.
type ScheduleService interface {
	SetShift(ctx context.Context, doctorID int32, in scheduling.Shift) (scheduling.Shift, error)
	ClearShift(ctx context.Context, doctorID int32, weekday int) error
	GetSchedule(ctx context.Context, doctorID int32) ([]scheduling.Shift, error)
}

var _ ScheduleService = (*scheduling.Service)(nil)

type ScheduleModule struct{ svc ScheduleService }

func NewScheduleModule(svc ScheduleService) ScheduleModule { return ScheduleModule{svc: svc} }

func (m ScheduleModule) Routes() []Route {
	// Doctor lets admins in too; canManageSchedule narrows it per doctor.
	return []Route{
		{Pattern: "GET /doctors/{id}/schedule", Access: Doctor, Handler: m.get},
		{Pattern: "PUT /doctors/{id}/schedule/{weekday}", Access: Doctor, Handler: m.put},
		{Pattern: "DELETE /doctors/{id}/schedule/{weekday}", Access: Doctor, Handler: m.clear},
	}
}

func (ScheduleModule) ErrorRules() []ErrorRule {
	return []ErrorRule{
		{Is: scheduling.ErrDoctorNotFound, Status: http.StatusNotFound, Code: "doctor_not_found"},
		{Is: scheduling.ErrShiftNotFound, Status: http.StatusNotFound, Code: "shift_not_found"},
		{Is: scheduling.ErrConflictingAppointments, Status: http.StatusConflict, Code: "schedule_conflict"},
	}
}

// canManageSchedule is the one place that decides who may read or change a doctor's schedule.
func canManageSchedule(p auth.Principal, doctorID int32) error {
	if p.IsAdmin() || (p.IsDoctor() && p.UserID == doctorID) {
		return nil
	}
	return apperr.ErrForbidden
}

func pathWeekday(r *http.Request) (int, error) {
	n, err := strconv.Atoi(r.PathValue("weekday"))
	if err != nil {
		return 0, badRequest("weekday must be an integer from 1 (Monday) to 5 (Friday)")
	}
	return n, nil // the range is validated by the service
}

type shiftRequest struct {
	Start      string `json:"start"`
	End        string `json:"end"`
	BreakStart string `json:"break_start"`
	BreakEnd   string `json:"break_end"`
}

type shiftResponse struct {
	Weekday    int    `json:"weekday"`
	Start      string `json:"start"`
	End        string `json:"end"`
	BreakStart string `json:"break_start,omitempty"`
	BreakEnd   string `json:"break_end,omitempty"`
}

func newShiftResponse(s scheduling.Shift) shiftResponse {
	return shiftResponse{
		Weekday: s.Weekday, Start: s.Start, End: s.End,
		BreakStart: s.BreakStart, BreakEnd: s.BreakEnd,
	}
}

func (m ScheduleModule) get(w http.ResponseWriter, r *http.Request) error {
	doctorID, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := canManageSchedule(principalFrom(r.Context()), doctorID); err != nil {
		return err
	}

	shifts, err := m.svc.GetSchedule(r.Context(), doctorID)
	if err != nil {
		return err
	}
	out := make([]shiftResponse, len(shifts))
	for i, s := range shifts {
		out[i] = newShiftResponse(s)
	}
	return writeJSON(w, http.StatusOK, map[string]any{"doctor_id": doctorID, "shifts": out})
}

func (m ScheduleModule) put(w http.ResponseWriter, r *http.Request) error {
	doctorID, err := pathID(r, "id")
	if err != nil {
		return err
	}
	weekday, err := pathWeekday(r)
	if err != nil {
		return err
	}
	// Authorise before reading the body, so a forbidden caller learns nothing about validation.
	if err := canManageSchedule(principalFrom(r.Context()), doctorID); err != nil {
		return err
	}

	var req shiftRequest
	if err := readJSON(w, r, &req); err != nil {
		return err
	}

	saved, err := m.svc.SetShift(r.Context(), doctorID, scheduling.Shift{
		Weekday: weekday, Start: req.Start, End: req.End,
		BreakStart: req.BreakStart, BreakEnd: req.BreakEnd,
	})
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, newShiftResponse(saved))
}

func (m ScheduleModule) clear(w http.ResponseWriter, r *http.Request) error {
	doctorID, err := pathID(r, "id")
	if err != nil {
		return err
	}
	weekday, err := pathWeekday(r)
	if err != nil {
		return err
	}
	if err := canManageSchedule(principalFrom(r.Context()), doctorID); err != nil {
		return err
	}

	if err := m.svc.ClearShift(r.Context(), doctorID, weekday); err != nil {
		return err
	}
	return noContent(w)
}
