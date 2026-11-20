-- +goose Up
-- The AO roles (#231): the Discord roles that mark a member as playing one
-- of the regiment's games, one guild-wide set saved the way the recording
-- roles are. A save of it is a change log entry under no hub.
CREATE TABLE ao_roles (
    guild_id TEXT   PRIMARY KEY,
    role_ids JSONB  NOT NULL DEFAULT '[]',
    version  BIGINT NOT NULL
);

ALTER TABLE change_log DROP CONSTRAINT change_log_action_check;
ALTER TABLE change_log ADD CONSTRAINT change_log_action_check
    CHECK (action IN ('create', 'register', 'update', 'remove', 'moderators', 'recording_roles', 'rank_roles', 'ao_roles'));

-- +goose Down
DELETE FROM change_log WHERE action = 'ao_roles';
ALTER TABLE change_log DROP CONSTRAINT change_log_action_check;
ALTER TABLE change_log ADD CONSTRAINT change_log_action_check
    CHECK (action IN ('create', 'register', 'update', 'remove', 'moderators', 'recording_roles', 'rank_roles'));

DROP TABLE ao_roles;
