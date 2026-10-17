-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = $1 AND deleted_at IS NULL;

-- name: CreateUser :one
INSERT INTO users (username, email, password_hash, firstname, lastname)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: CreatePatient :exec
INSERT INTO patients (patient_id) values ($1);