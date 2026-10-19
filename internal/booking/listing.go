package booking

import (
	"context"
	"fmt"
	"time"

	"clinic/internal/apperr"
	"clinic/internal/auth"
	"clinic/internal/scheduling"
	"clinic/internal/store"
)

const (
	defaultPageSize = 50
	maxPageSize     = 100
)

type PatientAppointment struct {
	ID         int32
	DoctorID   int32
	DoctorName string
	Start      time.Time // clinic time zone
	Status     store.AppointmentStatus
	Notes      string
}

type DoctorAppointment struct {
	ID          int32
	PatientID   int32
	PatientName string
	Start       time.Time // clinic time zone
	Status      store.AppointmentStatus
	Notes       string
}

// MyAppointments lists the calling patient's appointments. upcomingOnly keeps booked
// future ones, soonest first; otherwise everything is returned, newest first.
// limit defaults to 50 and is capped at 100.
func (s *Service) MyAppointments(ctx context.Context, p auth.Principal, upcomingOnly bool, limit, offset int) ([]PatientAppointment, error) {
	if !p.IsPatient() {
		return nil, apperr.ErrForbidden
	}
	if limit <= 0 {
		limit = defaultPageSize
	}
	limit = min(limit, maxPageSize)
	offset = max(offset, 0)

	rows, err := s.store.ListPatientAppointments(ctx, store.ListPatientAppointmentsParams{
		PatientID:    p.UserID,
		UpcomingOnly: upcomingOnly,
		PageSize:     int32(limit),
		PageOffset:   int32(offset),
	})
	if err != nil {
		return nil, fmt.Errorf("list appointments: %w", err)
	}

	out := make([]PatientAppointment, len(rows))
	for i, r := range rows {
		out[i] = PatientAppointment{
			ID:         r.AppointmentID,
			DoctorID:   r.DoctorID,
			DoctorName: store.FullName(r.DoctorFirstname, r.DoctorLastname),
			Start:      r.AppointmentStart.In(s.loc),
			Status:     r.Status,
			Notes:      r.Notes.String,
		}
	}
	return out, nil
}

// DoctorAgenda lists a doctor's appointments (all statuses) on a calendar day in clinic
// time; only the date part of day is used. Visible to that doctor and to administrators.
func (s *Service) DoctorAgenda(ctx context.Context, p auth.Principal, doctorID int32, day time.Time) ([]DoctorAppointment, error) {
	if !p.IsAdmin() && !(p.IsDoctor() && p.UserID == doctorID) {
		return nil, apperr.ErrForbidden
	}
	exists, err := s.store.DoctorExists(ctx, doctorID)
	if err != nil {
		return nil, fmt.Errorf("check doctor: %w", err)
	}
	if !exists {
		return nil, scheduling.ErrDoctorNotFound
	}

	y, mo, d := day.Date()
	from := time.Date(y, mo, d, 0, 0, 0, 0, s.loc)
	to := time.Date(y, mo, d+1, 0, 0, 0, 0, s.loc)

	rows, err := s.store.ListDoctorAppointmentsBetween(ctx, store.ListDoctorAppointmentsBetweenParams{
		DoctorID: doctorID, FromTime: from, ToTime: to,
	})
	if err != nil {
		return nil, fmt.Errorf("list agenda: %w", err)
	}

	out := make([]DoctorAppointment, len(rows))
	for i, r := range rows {
		out[i] = DoctorAppointment{
			ID:          r.AppointmentID,
			PatientID:   r.PatientID,
			PatientName: store.FullName(r.PatientFirstname, r.PatientLastname),
			Start:       r.AppointmentStart.In(s.loc),
			Status:      r.Status,
			Notes:       r.Notes.String,
		}
	}
	return out, nil
}
