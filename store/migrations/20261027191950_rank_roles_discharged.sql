-- +goose Up
-- The Discharged role (#893) saves with the rank roles. NULL is no save of
-- it yet, the state every row saved before this migration is in, so the
-- forum's Discharged role in code applies. An empty ID is a save of none.
--
-- The previous release runs on it as before. Its insert leaves the column
-- NULL and its update leaves it as it was, and it ignores the column, so
-- after a rollback a discharged trooper counts as a Cav member again.
ALTER TABLE rank_roles ADD COLUMN discharged_role_id TEXT;

-- +goose Down
ALTER TABLE rank_roles DROP COLUMN discharged_role_id;
