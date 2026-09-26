-- +goose Up
ALTER TABLE hubs ADD COLUMN renaming_allowed BOOLEAN NOT NULL DEFAULT TRUE;

-- +goose Down
ALTER TABLE hubs DROP COLUMN renaming_allowed;
