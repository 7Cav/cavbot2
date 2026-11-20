-- +goose Up
-- Warnings (#971): one row per /warn, listed by /infractions newest first.
-- message_id is the public notice's, set once it is posted.
CREATE TABLE warnings (
    id           BIGSERIAL   PRIMARY KEY,
    guild_id     TEXT        NOT NULL,
    member_id    TEXT        NOT NULL,
    moderator_id TEXT        NOT NULL,
    channel_id   TEXT        NOT NULL,
    message_id   TEXT        NOT NULL DEFAULT '',
    reason       TEXT        NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX warnings_member_idx ON warnings (guild_id, member_id, created_at DESC);

-- The warning immune roles: one guild-wide set whose holders /warn refuses,
-- saved the way the recording roles are. A save of it is a change log entry
-- under no hub.
CREATE TABLE warning_immune_roles (
    guild_id TEXT   PRIMARY KEY,
    role_ids JSONB  NOT NULL DEFAULT '[]',
    version  BIGINT NOT NULL
);

ALTER TABLE change_log DROP CONSTRAINT change_log_action_check;
ALTER TABLE change_log ADD CONSTRAINT change_log_action_check
    CHECK (action IN ('create', 'register', 'update', 'remove', 'moderators', 'recording_roles', 'rank_roles', 'ao_roles', 'forum_groups', 'warning_immune_roles'));

-- +goose Down
DELETE FROM change_log WHERE action = 'warning_immune_roles';
ALTER TABLE change_log DROP CONSTRAINT change_log_action_check;
ALTER TABLE change_log ADD CONSTRAINT change_log_action_check
    CHECK (action IN ('create', 'register', 'update', 'remove', 'moderators', 'recording_roles', 'rank_roles', 'ao_roles', 'forum_groups'));

DROP TABLE warning_immune_roles;
DROP TABLE warnings;
