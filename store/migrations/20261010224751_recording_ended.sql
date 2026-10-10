-- +goose Up
-- How a recording ended (#388): stopped, cap, empty, disk or recorder_left.
-- NULL while the recording runs, as stopped_at is.
ALTER TABLE recordings ADD COLUMN ended TEXT;

-- +goose Down
ALTER TABLE recordings DROP COLUMN ended;
