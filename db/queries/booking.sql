-- name: LockDoctorShared :one
SELECT d.doctor_id
FROM doctors d
         JOIN users u ON u.user_id = d.doctor_id
WHERE d.doctor_id = $1 AND u.deleted_at IS NULL
    FOR SHARE OF d;

-- name: DoctorExists :one
SELECT EXISTS (
    SELECT 1 FROM doctors d
                      JOIN users u ON u.user_id = d.doctor_id
    WHERE d.doctor_id = $1 AND u.deleted_at IS NULL
);

-- name: GetShift :one
SELECT * FROM doctor_schedules WHERE doctor_id = $1 AND weekday = $2;

-- name: IsOnLeave :one
SELECT EXISTS (
    SELECT 1 FROM doctor_time_off
    WHERE doctor_id = sqlc.arg(doctor_id)
      AND start_date <= sqlc.arg(day)::date
      AND end_date   >= sqlc.arg(day)::date
);

-- name: InsertAppointment :one
INSERT INTO appointments (doctor_id, patient_id, appointment_start)
VALUES ($1, $2, $3)
RETURNING *;

-- name: ListBookedStarts :many
SELECT appointment_start FROM appointments
WHERE doctor_id = sqlc.arg(doctor_id)
  AND status = 'booked'
  AND appointment_start >= sqlc.arg(from_time)::timestamptz
  AND appointment_start <  sqlc.arg(to_time)::timestamptz
ORDER BY appointment_start;

-- name: CancelAppointment :execrows
UPDATE appointments
SET status = 'cancelled',
    notes  = sqlc.arg(notes)::text
WHERE appointment_id = sqlc.arg(appointment_id)
  AND patient_id     = sqlc.arg(patient_id)
  AND status = 'booked'
  AND appointment_start > now();

-- name: GetPatientAppointment :one
SELECT * FROM appointments WHERE appointment_id = $1 AND patient_id = $2;

-- name: GetPatientAppointmentForUpdate :one
SELECT * FROM appointments
WHERE appointment_id = $1 AND patient_id = $2
    FOR UPDATE;

-- name: RescheduleAppointment :one
UPDATE appointments SET appointment_start = $2
WHERE appointment_id = $1
    RETURNING *;

-- name: ListPatientAppointments :many
SELECT a.appointment_id, a.doctor_id,
       u.firstname AS doctor_firstname, u.lastname AS doctor_lastname,
       a.appointment_start, a.status, a.notes
FROM appointments a
         JOIN users u ON u.user_id = a.doctor_id
WHERE a.patient_id = sqlc.arg(patient_id)
  AND (NOT sqlc.arg(upcoming_only)::boolean
       OR (a.status = 'booked' AND a.appointment_start > now()))
-- upcoming: soonest first; history: newest first
ORDER BY CASE WHEN sqlc.arg(upcoming_only)::boolean THEN a.appointment_start END ASC,
         a.appointment_start DESC
    LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: ListDoctorAppointmentsBetween :many
SELECT a.appointment_id, a.patient_id,
       u.firstname AS patient_firstname, u.lastname AS patient_lastname,
       a.appointment_start, a.status, a.notes
FROM appointments a
         JOIN users u ON u.user_id = a.patient_id
WHERE a.doctor_id = sqlc.arg(doctor_id)
  AND a.appointment_start >= sqlc.arg(from_time)::timestamptz
  AND a.appointment_start <  sqlc.arg(to_time)::timestamptz
ORDER BY a.appointment_start;

-- name: MarkAppointmentOutcome :execrows
UPDATE appointments
SET status = sqlc.arg(status)::appointment_status,
    notes  = COALESCE(sqlc.narg(notes)::text, notes)
WHERE appointment_id = sqlc.arg(appointment_id)
  AND doctor_id      = sqlc.arg(doctor_id)
  AND status = 'booked'
  AND appointment_start <= now();

-- name: GetDoctorAppointment :one
SELECT * FROM appointments WHERE appointment_id = $1 AND doctor_id = $2;

-- name: ListPendingOutcomes :many
SELECT a.appointment_id, a.patient_id,
       u.firstname AS patient_firstname, u.lastname AS patient_lastname,
       a.appointment_start, a.status, a.notes
FROM appointments a
         JOIN users u ON u.user_id = a.patient_id
WHERE a.doctor_id = sqlc.arg(doctor_id)
  AND a.status = 'booked'
  AND a.appointment_start <= now()
ORDER BY a.appointment_start
    LIMIT sqlc.arg(page_size);
