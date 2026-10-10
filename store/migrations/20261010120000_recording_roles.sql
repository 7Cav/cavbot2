-- +goose Up
-- The recording roles (#383): one guild-wide set, versioned apart from the
-- guild-wide moderator roles so a save of one never makes the other's form
-- stale. A save of it is a change log entry under no hub.
CREATE TABLE recording_roles (
    guild_id TEXT   PRIMARY KEY,
    role_ids JSONB  NOT NULL DEFAULT '[]',
    version  BIGINT NOT NULL
);

ALTER TABLE change_log DROP CONSTRAINT change_log_action_check;
ALTER TABLE change_log ADD CONSTRAINT change_log_action_check
    CHECK (action IN ('create', 'register', 'update', 'remove', 'moderators', 'recording_roles'));

-- +goose Down
DELETE FROM change_log WHERE action = 'recording_roles';
ALTER TABLE change_log DROP CONSTRAINT change_log_action_check;
ALTER TABLE change_log ADD CONSTRAINT change_log_action_check
    CHECK (action IN ('create', 'register', 'update', 'remove', 'moderators'));

DROP TABLE recording_roles;
