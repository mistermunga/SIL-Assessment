-- name: LockDoctor :one
SELECT doctor_id FROM doctors WHERE doctor_id = $1 FOR UPDATE;

-- name: UpsertShift :one
INSERT INTO doctor_schedules (doctor_id, weekday, start_time, end_time, break_start, break_end)
VALUES ($1, $2, $3, $4, $5, $6)
    ON CONFLICT (doctor_id, weekday) DO UPDATE
                                            SET start_time  = EXCLUDED.start_time,
                                            end_time    = EXCLUDED.end_time,
                                            break_start = EXCLUDED.break_start,
                                            break_end   = EXCLUDED.break_end
                                            RETURNING *;

-- name: DeleteShift :execrows
DELETE FROM doctor_schedules WHERE doctor_id = $1 AND weekday = $2;

-- name: ListShifts :many
SELECT * FROM doctor_schedules WHERE doctor_id = $1 ORDER BY weekday;

-- name: ListFutureBookedStarts :many
SELECT appointment_start FROM appointments
WHERE doctor_id = $1 AND status = 'booked' AND appointment_start > now()
ORDER BY appointment_start;