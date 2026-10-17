
-- +goose Up
CREATE TYPE appointment_status AS ENUM ('booked', 'cancelled', 'no_show', 'completed');

CREATE TABLE users (
    user_id       int GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username      varchar(20) NOT NULL UNIQUE,
    email         varchar(50) NOT NULL UNIQUE,
    password_hash text        NOT NULL,
    firstname     text,
    lastname      text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    deleted_at    timestamptz
);

CREATE TABLE doctors (
    doctor_id int PRIMARY KEY REFERENCES users (user_id),
    is_admin  boolean NOT NULL DEFAULT false
);

CREATE TABLE patients (
    patient_id int PRIMARY KEY REFERENCES users (user_id)
);

CREATE TABLE doctor_schedules (
    schedule_id int GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    doctor_id   int      NOT NULL REFERENCES doctors (doctor_id),
    weekday     smallint NOT NULL,
    start_time  time     NOT NULL,
    end_time    time     NOT NULL,
    break_start time,
    break_end   time,
    CONSTRAINT uq_doctor_weekday UNIQUE (doctor_id, weekday),
    CONSTRAINT weekday_valid CHECK (weekday BETWEEN 1 AND 5),
    CONSTRAINT shift_valid   CHECK (start_time < end_time),
    CONSTRAINT break_both_or_neither
        CHECK ((break_start IS NULL) = (break_end IS NULL)),
    CONSTRAINT break_valid
        CHECK (break_start IS NULL
            OR (start_time <= break_start
                AND break_start < break_end
                AND break_end <= end_time
                AND break_end - break_start <= interval '1 hour')),
    CONSTRAINT times_on_half_hour
        CHECK (mod(extract(epoch FROM start_time), 1800) = 0
           AND mod(extract(epoch FROM end_time),   1800) = 0
           AND (break_start IS NULL
                OR (mod(extract(epoch FROM break_start), 1800) = 0
                AND mod(extract(epoch FROM break_end),   1800) = 0)))
);

CREATE TABLE doctor_time_off (
    time_off_id int GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    doctor_id   int         NOT NULL REFERENCES doctors (doctor_id),
    start_date  date        NOT NULL,
    end_date    date        NOT NULL,
    reason      text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT time_off_valid CHECK (start_date <= end_date)
);
CREATE INDEX ix_time_off_doctor ON doctor_time_off (doctor_id, start_date, end_date);

CREATE TABLE appointments (
    appointment_id    int GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    doctor_id         int                NOT NULL REFERENCES doctors (doctor_id),
    patient_id        int                NOT NULL REFERENCES patients (patient_id),
    appointment_start timestamptz        NOT NULL,
    status            appointment_status NOT NULL DEFAULT 'booked',
    created_at        timestamptz        NOT NULL DEFAULT now(),
    CONSTRAINT slot_on_half_hour
        CHECK (mod(extract(epoch FROM appointment_start), 1800) = 0)
);

-- double-booking guard
CREATE UNIQUE INDEX uq_doctor_active_slot
    ON appointments (doctor_id, appointment_start)
    WHERE status = 'booked';

CREATE INDEX ix_patient_appointments
    ON appointments (patient_id, appointment_start);

-- +goose Down
DROP TABLE appointments, doctor_time_off, doctor_schedules, patients, doctors, users;
DROP TYPE appointment_status;
