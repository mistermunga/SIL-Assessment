-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = $1 AND deleted_at IS NULL;

-- name: CreateUser :one
INSERT INTO users (username, email, password_hash, firstname, lastname)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: CreatePatient :exec
INSERT INTO patients (patient_id) values ($1);

-- name: CreateDoctor :exec
INSERT INTO doctors (doctor_id, is_admin) values ($1, $2);

-- name: GetPrincipal :one
SELECT u.user_id,
       u.username,
       (p.patient_id IS NOT NULL)::boolean AS is_patient,
       (d.doctor_id  IS NOT NULL)::boolean AS is_doctor,
       COALESCE(d.is_admin, false)         AS is_admin
FROM users u
         LEFT JOIN patients p ON p.patient_id = u.user_id
         LEFT JOIN doctors  d ON d.doctor_id  = u.user_id
WHERE u.user_id = $1 AND u.deleted_at IS NULL;

-- name: ListDoctors :many
SELECT d.doctor_id, u.firstname, u.lastname
FROM doctors d
         JOIN users u ON u.user_id = d.doctor_id
WHERE u.deleted_at IS NULL
ORDER BY u.lastname NULLS LAST, u.firstname NULLS LAST, d.doctor_id;

-- name: AdminExists :one
SELECT EXISTS (
    SELECT 1 FROM doctors d
                      JOIN users u ON u.user_id = d.doctor_id
    WHERE d.is_admin AND u.deleted_at IS NULL
);
