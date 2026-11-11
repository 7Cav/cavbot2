-- +goose Up
-- Discord activity for the Metrics page (#949): IDs and times only, never
-- message text. Raw rows last 180 days, metric_weeks has no limit.
CREATE TABLE metric_state (
    only_row      BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (only_row),
    started_at    TIMESTAMPTZ NOT NULL,   -- first start: voice, joins and leaves count from here
    backfilled_at TIMESTAMPTZ             -- null until the 12-week message backfill finished
);
CREATE TABLE metric_messages (
    message_id    TEXT PRIMARY KEY,                 -- backfill and live overlap; PK dedups
    user_id       TEXT NOT NULL,
    category_id   TEXT NOT NULL DEFAULT '',         -- category of the channel (a thread's parent) when sent; '' none
    sent_at       TIMESTAMPTZ NOT NULL,
    addressed_ids TEXT[] NOT NULL DEFAULT '{}'      -- reply target and mentioned users, author excluded
);
CREATE INDEX metric_messages_sent_at ON metric_messages (sent_at);
CREATE INDEX metric_messages_user_sent ON metric_messages (user_id, sent_at);
CREATE INDEX metric_messages_addressed ON metric_messages USING GIN (addressed_ids);
CREATE TABLE metric_voice_sessions (
    id          BIGSERIAL PRIMARY KEY,
    user_id     TEXT NOT NULL,
    channel_id  TEXT NOT NULL,
    category_id TEXT NOT NULL DEFAULT '',
    role_ids    TEXT[] NOT NULL DEFAULT '{}',       -- section roles held at session start
    started_at  TIMESTAMPTZ NOT NULL,
    ended_at    TIMESTAMPTZ,                         -- null while open
    CHECK (ended_at IS NULL OR ended_at >= started_at)
);
CREATE UNIQUE INDEX metric_voice_sessions_open ON metric_voice_sessions (user_id) WHERE ended_at IS NULL;
CREATE INDEX metric_voice_sessions_started ON metric_voice_sessions (started_at);
CREATE TABLE metric_member_events (
    user_id TEXT NOT NULL,
    kind    TEXT NOT NULL CHECK (kind IN ('join', 'leave')),
    at      TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, kind, at)
);
CREATE INDEX metric_member_events_at ON metric_member_events (at);
CREATE TABLE metric_weeks (
    week          DATE NOT NULL,                     -- Monday, UTC
    category_id   TEXT NOT NULL,                     -- '' = whole server
    messages      INTEGER NOT NULL,
    voice_seconds BIGINT NOT NULL,
    voice_people  INTEGER NOT NULL,
    active        INTEGER NOT NULL,
    PRIMARY KEY (week, category_id)
);

-- +goose Down
DROP TABLE metric_weeks;
DROP TABLE metric_member_events;
DROP TABLE metric_voice_sessions;
DROP TABLE metric_messages;
DROP TABLE metric_state;
