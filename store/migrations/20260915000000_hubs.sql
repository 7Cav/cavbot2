-- +goose Up
CREATE TABLE hubs (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    guild_id           TEXT        NOT NULL,
    hub_channel_id     TEXT        NOT NULL UNIQUE,
    base_string        TEXT        NOT NULL,
    permission_source  TEXT        NOT NULL CHECK (permission_source IN ('category', 'hub_channel')),
    moderator_role_ids TEXT[]      NOT NULL DEFAULT '{}',
    user_limit         INTEGER     NOT NULL DEFAULT 0,
    bitrate            INTEGER     NOT NULL DEFAULT 64000,
    enabled            BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE spawned_channels (
    channel_id    TEXT        PRIMARY KEY,
    hub_id        BIGINT      REFERENCES hubs (id) ON DELETE SET NULL,
    number        INTEGER     NOT NULL,
    owner_user_id TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE spawned_channels;
DROP TABLE hubs;
