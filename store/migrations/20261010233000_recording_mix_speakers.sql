-- +goose Up
-- A recording's mix state and speakers (#389), both written at its stop.
-- mix is processing, ready or failed, and NULL while the recording runs.
-- speakers is a JSON array of {"id", "display_name"} objects, empty while it
-- runs.
ALTER TABLE recordings ADD COLUMN mix TEXT;
ALTER TABLE recordings ADD COLUMN speakers JSONB NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE recordings DROP COLUMN speakers;
ALTER TABLE recordings DROP COLUMN mix;
