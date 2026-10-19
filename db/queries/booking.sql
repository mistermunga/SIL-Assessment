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