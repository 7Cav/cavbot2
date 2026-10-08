-- +goose Up
-- A removal of a Foxhole role from selected members is a Foxhole action,
-- and its report is its entry in the Foxhole change log.
ALTER TABLE foxhole_change_log DROP CONSTRAINT foxhole_change_log_action_check;
ALTER TABLE foxhole_change_log ADD CONSTRAINT foxhole_change_log_action_check
    CHECK (action IN ('note', 'approve', 'clear_approval', 'purge', 're_add', 'removal'));

-- +goose Down
DELETE FROM foxhole_change_log WHERE action = 'removal';
ALTER TABLE foxhole_change_log DROP CONSTRAINT foxhole_change_log_action_check;
ALTER TABLE foxhole_change_log ADD CONSTRAINT foxhole_change_log_action_check
    CHECK (action IN ('note', 'approve', 'clear_approval', 'purge', 're_add'));
