-- +goose Up
CREATE TABLE guild_settings (
    guild_id           TEXT  PRIMARY KEY,
    moderator_role_ids JSONB NOT NULL DEFAULT '[]'
);

-- +goose Down
DROP TABLE guild_settings;
