-- +goose Up
-- The winged aviators (#869): each member someone gave the Winged Aviator
-- role, by Discord user ID. A discharge or leaving the server takes only the
-- role, so the row stays until a person removes the role.
CREATE TABLE winged_aviators (
    guild_id TEXT NOT NULL,
    user_id  TEXT NOT NULL,
    PRIMARY KEY (guild_id, user_id)
);

-- +goose Down
DROP TABLE winged_aviators;
