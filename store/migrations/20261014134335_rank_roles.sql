-- +goose Up
-- The rank roles (#680): the role that counts as each rank on the rank
-- ladder, one row for the whole ladder, keyed by rank abbreviation with an
-- empty ID for a rank with none. A guild with no row uses the ladder's role
-- IDs in code. A save of it is a change log entry under no hub.
CREATE TABLE rank_roles (
    guild_id TEXT   PRIMARY KEY,
    roles    JSONB  NOT NULL DEFAULT '{}',
    version  BIGINT NOT NULL
);

ALTER TABLE change_log DROP CONSTRAINT change_log_action_check;
ALTER TABLE change_log ADD CONSTRAINT change_log_action_check
    CHECK (action IN ('create', 'register', 'update', 'remove', 'moderators', 'recording_roles', 'rank_roles'));

-- +goose Down
DELETE FROM change_log WHERE action = 'rank_roles';
ALTER TABLE change_log DROP CONSTRAINT change_log_action_check;
ALTER TABLE change_log ADD CONSTRAINT change_log_action_check
    CHECK (action IN ('create', 'register', 'update', 'remove', 'moderators', 'recording_roles'));

DROP TABLE rank_roles;
