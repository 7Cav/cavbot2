-- +goose Up
-- Recordings (#386): one row per recording, written when a recorder joins
-- and closed when it leaves. stopped_at is NULL while the recording runs.
CREATE TABLE recordings (
    id           BIGSERIAL   PRIMARY KEY,
    guild_id     TEXT        NOT NULL,
    channel_id   TEXT        NOT NULL,
    channel_name TEXT        NOT NULL,
    starter_id   TEXT        NOT NULL,
    title        TEXT        NOT NULL,
    recorder_id  TEXT        NOT NULL,
    started_at   TIMESTAMPTZ NOT NULL,
    stopped_at   TIMESTAMPTZ
);

-- +goose Down
DROP TABLE recordings;
