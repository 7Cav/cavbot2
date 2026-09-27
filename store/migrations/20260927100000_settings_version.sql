-- +goose Up
ALTER TABLE hubs ADD COLUMN version BIGINT NOT NULL DEFAULT 1;
ALTER TABLE guild_settings ADD COLUMN version BIGINT NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE guild_settings DROP COLUMN version;
ALTER TABLE hubs DROP COLUMN version;
