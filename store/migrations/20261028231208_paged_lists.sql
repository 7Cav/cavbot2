-- +goose Up
-- The saved pages of a long /loa or /awol list (#824, ADR 0020): one row per
-- run, keyed by the unguessable key its buttons name, with who ran it, when,
-- and every page as a JSON array of {title, description, footer}. A sweep
-- deletes a run 30 days after it.
CREATE TABLE paged_lists (
    key       TEXT        PRIMARY KEY,
    runner_id TEXT        NOT NULL,
    ran_at    TIMESTAMPTZ NOT NULL,
    pages     JSONB       NOT NULL
);

CREATE INDEX paged_lists_ran_at ON paged_lists (ran_at);

-- +goose Down
DROP TABLE paged_lists;
