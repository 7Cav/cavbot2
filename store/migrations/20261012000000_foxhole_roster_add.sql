-- +goose Up
-- An add of a validated internal unit's roster to Internal from the Foxhole
-- page is a Foxhole action, and its report is its entry in the Foxhole
-- change log.
ALTER TABLE foxhole_change_log DROP CONSTRAINT foxhole_change_log_action_check;
ALTER TABLE foxhole_change_log ADD CONSTRAINT foxhole_change_log_action_check
    CHECK (action IN ('note', 'approve', 'clear_approval', 'purge', 're_add', 'removal', 'add', 'roster_add'));

-- +goose Down
DELETE FROM foxhole_change_log WHERE action = 'roster_add';
ALTER TABLE foxhole_change_log DROP CONSTRAINT foxhole_change_log_action_check;
ALTER TABLE foxhole_change_log ADD CONSTRAINT foxhole_change_log_action_check
    CHECK (action IN ('note', 'approve', 'clear_approval', 'purge', 're_add', 'removal', 'add'));
