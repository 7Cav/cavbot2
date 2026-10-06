-- +goose Up
CREATE TABLE foxhole_members (
    guild_id          TEXT        NOT NULL,
    member_id         TEXT        NOT NULL,
    note              TEXT        NOT NULL DEFAULT '',
    approved          BOOLEAN     NOT NULL DEFAULT FALSE,
    last_display_name TEXT        NOT NULL,
    last_username     TEXT        NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (guild_id, member_id)
);

CREATE TABLE foxhole_change_log (
    id             BIGSERIAL   PRIMARY KEY,
    forum_user_id  INTEGER     NOT NULL,
    forum_username TEXT        NOT NULL,
    at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    action         TEXT        NOT NULL CHECK (action IN ('note')),
    diff           JSONB       NOT NULL
);

-- +goose Down
DROP TABLE foxhole_change_log;
DROP TABLE foxhole_members;
