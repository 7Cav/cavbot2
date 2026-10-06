-- +goose Up
-- A Foxhole action's report is its entry in the Foxhole change log. report
-- is NULL on a save's entry, and on a report says whether its action is
-- still running.
ALTER TABLE foxhole_change_log ADD COLUMN report TEXT CHECK (report IN ('running', 'ended'));
ALTER TABLE foxhole_change_log DROP CONSTRAINT foxhole_change_log_action_check;
ALTER TABLE foxhole_change_log ADD CONSTRAINT foxhole_change_log_action_check
    CHECK (action IN ('note', 'approve', 'clear_approval', 'purge'));
CREATE INDEX foxhole_change_log_reports ON foxhole_change_log (id DESC) WHERE report IS NOT NULL;

-- +goose Down
DROP INDEX foxhole_change_log_reports;
DELETE FROM foxhole_change_log WHERE action = 'purge';
ALTER TABLE foxhole_change_log DROP CONSTRAINT foxhole_change_log_action_check;
ALTER TABLE foxhole_change_log ADD CONSTRAINT foxhole_change_log_action_check
    CHECK (action IN ('note', 'approve', 'clear_approval'));
ALTER TABLE foxhole_change_log DROP COLUMN report;
