package httpapi

import (
	"context"
	"net/http"
	"time"

	"clinic/internal/apperr"
	"clinic/internal/auth"
	"clinic/internal/booking"
	"clinic/internal/scheduling"
	"clinic/internal/store"
)

const slotLength = scheduling.SlotMinutes * time.Minute

// BookingService is what this module needs from the booking service.
type BookingService interface {
	FreeSlots(ctx context.Context, doctorID int32, day time.Time) ([]time.Time, error)
	Book(ctx context.Context, p auth.Principal, doctorID int32, start time.Time) (store.Appointment, error)
	Cancel(ctx context.Context, p auth.Principal, appointmentID int32, reason string) error
	Reschedule(ctx context.Context, p auth.Principal, appointmentID int32, newStart time.Time) (store.Appointment, error)
	MyAppointments(ctx context.Context, p auth.Principal, upcomingOnly bool, limit, offset int) ([]booking.PatientAppointment, error)
	DoctorAgenda(ctx context.Context, p auth.Principal, doctorID int32, day time.Time) ([]booking.DoctorAppointment, error)
	MarkOutcome(ctx context.Context, p auth.Principal, appointmentID int32, outcome store.AppointmentStatus, notes string) error
	PendingOutcomes(ctx context.Context, p auth.Principal, limit int) ([]booking.DoctorAppointment, error)
}

// Compile-time check: the real service satisfies the interface.
var _ BookingService = (*booking.Service)(nil)

type BookingModule struct {
	svc BookingService
	loc *time.Location // clinic zone: dates are read in it and times are shown in it
}

func NewBookingModule(svc BookingService, loc *time.Location) BookingModule {
	return BookingModule{svc: svc, loc: loc}
}

func (m BookingModule) Routes() []Route {
	return []Route{
		{Pattern: "POST /appointments", Access: Patient, Handler: m.book},
		{Pattern: "GET /appointments", Access: Patient, Handler: m.mine},
		{Pattern: "GET /patients/{id}/appointments", Access: Patient, Handler: m.patientUpcoming},
		{Pattern: "PATCH /appointments/{id}/cancel", Access: Patient, Handler: m.cancel},
		{Pattern: "PATCH /appointments/{id}/reschedule", Access: Patient, Handler: m.reschedule},
		{Pattern: "GET /doctors/{id}/availability", Access: Authenticated, Handler: m.availability},
		// The service decides which doctor's agenda a caller may read (own, or any for an admin).
		{Pattern: "GET /doctors/{id}/appointments", Access: Doctor, Handler: m.agenda},
		{Pattern: "PATCH /appointments/{id}/outcome", Access: Doctor, Handler: m.outcome},
		{Pattern: "GET /doctors/me/pending-outcomes", Access: Doctor, Handler: m.pending},
	}
}

func (BookingModule) ErrorRules() []ErrorRule {
	return []ErrorRule{
		{Is: booking.ErrSlotUnavailable, Status: http.StatusUnprocessableEntity, Code: "slot_unavailable"},
		{Is: booking.ErrSlotTaken, Status: http.StatusConflict, Code: "slot_taken"},
		{Is: booking.ErrPatientBusy, Status: http.StatusConflict, Code: "patient_busy"},
		{Is: booking.ErrNotFound, Status: http.StatusNotFound, Code: "appointment_not_found"},
		{Is: booking.ErrAlreadyCancelled, Status: http.StatusConflict, Code: "already_cancelled"},
		{Is: booking.ErrNotCancellable, Status: http.StatusConflict, Code: "not_cancellable"},
		{Is: booking.ErrNotReschedulable, Status: http.StatusConflict, Code: "not_reschedulable"},
		{Is: booking.ErrNotMarkable, Status: http.StatusConflict, Code: "not_markable"},
		{Is: scheduling.ErrDoctorNotFound, Status: http.StatusNotFound, Code: "doctor_not_found"},
	}
}

// ---- responses ----

type appointmentResponse struct {
	ID        int32     `json:"id"`
	DoctorID  int32     `json:"doctor_id"`
	PatientID int32     `json:"patient_id"`
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Status    string    `json:"status"`
	Notes     string    `json:"notes,omitempty"`
}

func (m BookingModule) appointment(a store.Appointment) appointmentResponse {
	start := a.AppointmentStart.In(m.loc)
	return appointmentResponse{
		ID:        a.AppointmentID,
		DoctorID:  a.DoctorID,
		PatientID: a.PatientID,
		Start:     start,
		End:       start.Add(slotLength),
		Status:    string(a.Status),
		Notes:     a.Notes.String,
	}
}

type patientEntry struct {
	ID         int32     `json:"id"`
	DoctorID   int32     `json:"doctor_id"`
	DoctorName string    `json:"doctor_name"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Status     string    `json:"status"`
	Notes      string    `json:"notes,omitempty"`
}

func (m BookingModule) patientEntries(list []booking.PatientAppointment) []patientEntry {
	out := make([]patientEntry, len(list)) // never nil, so an empty list encodes as []
	for i, a := range list {
		start := a.Start.In(m.loc)
		out[i] = patientEntry{
			ID: a.ID, DoctorID: a.DoctorID, DoctorName: a.DoctorName,
			Start: start, End: start.Add(slotLength), Status: string(a.Status), Notes: a.Notes,
		}
	}
	return out
}

type doctorEntry struct {
	ID          int32     `json:"id"`
	PatientID   int32     `json:"patient_id"`
	PatientName string    `json:"patient_name"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	Status      string    `json:"status"`
	Notes       string    `json:"notes,omitempty"`
}

func (m BookingModule) doctorEntries(list []booking.DoctorAppointment) []doctorEntry {
	out := make([]doctorEntry, len(list))
	for i, a := range list {
		start := a.Start.In(m.loc)
		out[i] = doctorEntry{
			ID: a.ID, PatientID: a.PatientID, PatientName: a.PatientName,
			Start: start, End: start.Add(slotLength), Status: string(a.Status), Notes: a.Notes,
		}
	}
	return out
}

type slotResponse struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// ---- patient endpoints ----

type bookRequest struct {
	DoctorID int32  `json:"doctor_id"`
	Start    string `json:"start"`
}

func (m BookingModule) patientUpcoming(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	p := principalFrom(r.Context())
	if id != p.UserID {
		return apperr.ErrForbidden // a patient reads only their own
	}
	limit, err := intParam(r, "limit")
	if err != nil {
		return err
	}
	offset, err := intParam(r, "offset")
	if err != nil {
		return err
	}

	list, err := m.svc.MyAppointments(r.Context(), p, true, limit, offset)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]any{"patient_id": id, "appointments": m.patientEntries(list)})
}

func (m BookingModule) book(w http.ResponseWriter, r *http.Request) error {
	var req bookRequest
	if err := readJSON(w, r, &req); err != nil {
		return err
	}

	problems := map[string]string{}
	if req.DoctorID <= 0 {
		problems["doctor_id"] = "is required"
	}
	start, ok := parseTimestamp(req.Start)
	if !ok {
		problems["start"] = timestampHint
	}
	if len(problems) > 0 {
		return &apperr.ValidationError{Problems: problems}
	}

	a, err := m.svc.Book(r.Context(), principalFrom(r.Context()), req.DoctorID, start)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusCreated, m.appointment(a))
}

func (m BookingModule) mine(w http.ResponseWriter, r *http.Request) error {
	upcoming, err := boolParam(r, "upcoming")
	if err != nil {
		return err
	}
	limit, err := intParam(r, "limit")
	if err != nil {
		return err
	}
	offset, err := intParam(r, "offset")
	if err != nil {
		return err
	}

	list, err := m.svc.MyAppointments(r.Context(), principalFrom(r.Context()), upcoming, limit, offset)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]any{"appointments": m.patientEntries(list)})
}

type cancelRequest struct {
	Reason string `json:"reason"`
}

func (m BookingModule) cancel(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var req cancelRequest
	if err := readJSON(w, r, &req); err != nil {
		return err
	}

	if err := m.svc.Cancel(r.Context(), principalFrom(r.Context()), id, req.Reason); err != nil {
		return err
	}
	return noContent(w)
}

type rescheduleRequest struct {
	Start string `json:"start"`
}

func (m BookingModule) reschedule(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var req rescheduleRequest
	if err := readJSON(w, r, &req); err != nil {
		return err
	}
	start, ok := parseTimestamp(req.Start)
	if !ok {
		return invalid("start", timestampHint)
	}

	a, err := m.svc.Reschedule(r.Context(), principalFrom(r.Context()), id, start)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, m.appointment(a))
}

// ---- availability (any signed-in user) ----

func (m BookingModule) availability(w http.ResponseWriter, r *http.Request) error {
	doctorID, err := pathID(r, "id")
	if err != nil {
		return err
	}
	day, err := dateParam(r, "date", m.loc)
	if err != nil {
		return err
	}

	slots, err := m.svc.FreeSlots(r.Context(), doctorID, day)
	if err != nil {
		return err
	}
	out := make([]slotResponse, len(slots))
	for i, s := range slots {
		s = s.In(m.loc)
		out[i] = slotResponse{Start: s, End: s.Add(slotLength)}
	}
	return writeJSON(w, http.StatusOK, map[string]any{
		"doctor_id": doctorID,
		"date":      day.Format(dateLayout),
		"slots":     out,
	})
}

// ---- doctor endpoints ----

func (m BookingModule) agenda(w http.ResponseWriter, r *http.Request) error {
	doctorID, err := pathID(r, "id")
	if err != nil {
		return err
	}
	day, err := dateParam(r, "date", m.loc)
	if err != nil {
		return err
	}

	list, err := m.svc.DoctorAgenda(r.Context(), principalFrom(r.Context()), doctorID, day)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]any{
		"doctor_id":    doctorID,
		"date":         day.Format(dateLayout),
		"appointments": m.doctorEntries(list),
	})
}

type outcomeRequest struct {
	Outcome string `json:"outcome"`
	Notes   string `json:"notes"`
}

func (m BookingModule) outcome(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var req outcomeRequest
	if err := readJSON(w, r, &req); err != nil {
		return err
	}

	// The service rejects anything other than completed or no_show.
	err = m.svc.MarkOutcome(r.Context(), principalFrom(r.Context()), id,
		store.AppointmentStatus(req.Outcome), req.Notes)
	if err != nil {
		return err
	}
	return noContent(w)
}

func (m BookingModule) pending(w http.ResponseWriter, r *http.Request) error {
	limit, err := intParam(r, "limit")
	if err != nil {
		return err
	}
	list, err := m.svc.PendingOutcomes(r.Context(), principalFrom(r.Context()), limit)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]any{"appointments": m.doctorEntries(list)})
}
