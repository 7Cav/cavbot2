-- +goose Up
-- The forum groups: the Foxhole group and the Joiners groups the panel's
-- group check reads, set on the Settings page. A save of them is a change
-- log entry under no hub.
CREATE TABLE forum_groups (
    guild_id          TEXT    PRIMARY KEY,
    foxhole_group_id  INTEGER NOT NULL,
    joiners_group_ids JSONB   NOT NULL DEFAULT '[]',
    version           BIGINT  NOT NULL
);

ALTER TABLE change_log DROP CONSTRAINT change_log_action_check;
ALTER TABLE change_log ADD CONSTRAINT change_log_action_check
    CHECK (action IN ('create', 'register', 'update', 'remove', 'moderators', 'recording_roles', 'rank_roles', 'ao_roles', 'forum_groups'));

-- +goose Down
DELETE FROM change_log WHERE action = 'forum_groups';
ALTER TABLE change_log DROP CONSTRAINT change_log_action_check;
ALTER TABLE change_log ADD CONSTRAINT change_log_action_check
    CHECK (action IN ('create', 'register', 'update', 'remove', 'moderators', 'recording_roles', 'rank_roles', 'ao_roles'));

DROP TABLE forum_groups;
