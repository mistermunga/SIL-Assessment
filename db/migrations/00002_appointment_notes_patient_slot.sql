-- +goose Up
ALTER TABLE appointments
    ADD COLUMN notes text,
    ADD CONSTRAINT notes_length CHECK (char_length(notes) <= 500);

-- a patient can't hold two booked appointments starting at the same time
CREATE UNIQUE INDEX uq_patient_active_slot
    ON appointments (patient_id, appointment_start)
    WHERE status = 'booked';

-- +goose Down
DROP INDEX uq_patient_active_slot;
ALTER TABLE appointments
    DROP CONSTRAINT notes_length,
    DROP COLUMN notes;