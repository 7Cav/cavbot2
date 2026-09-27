-- +goose Up
ALTER TABLE hubs ADD COLUMN delete_delay_minutes INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE hubs DROP COLUMN delete_delay_minutes;
