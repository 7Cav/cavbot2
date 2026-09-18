-- +goose Up
CREATE TABLE change_log (
    id             BIGSERIAL   PRIMARY KEY,
    hub_id         BIGINT      REFERENCES hubs (id) ON DELETE SET NULL,
    forum_user_id  INTEGER     NOT NULL,
    forum_username TEXT        NOT NULL,
    at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    action         TEXT        NOT NULL CHECK (action IN ('create', 'register', 'update', 'remove', 'moderators')),
    diff           JSONB       NOT NULL
);

CREATE INDEX change_log_hub_id_id ON change_log (hub_id, id DESC);

-- +goose Down
DROP TABLE change_log;
