-- +goose Up
-- The winged aviators of each Winged Aviator role, one role per game. This
-- replaces the table rather than adding to it, against the store's additive
-- rule: no host with live members ever set the role, so the table is empty
-- there, and a release before this one never touches it with the role unset.
DROP TABLE winged_aviators;
CREATE TABLE winged_aviators (
    guild_id TEXT NOT NULL,
    role_id  TEXT NOT NULL,
    user_id  TEXT NOT NULL,
    PRIMARY KEY (guild_id, role_id, user_id)
);

-- +goose Down
DROP TABLE winged_aviators;
CREATE TABLE winged_aviators (
    guild_id TEXT NOT NULL,
    user_id  TEXT NOT NULL,
    PRIMARY KEY (guild_id, user_id)
);
