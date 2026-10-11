-- +goose Up
-- The message ID of a recording's notice (#393), in the recorded channel's
-- text chat, written at start, so the process that recovers a recording
-- cut short by a crash can edit it.
ALTER TABLE recordings ADD COLUMN notice_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE recordings DROP COLUMN notice_id;
