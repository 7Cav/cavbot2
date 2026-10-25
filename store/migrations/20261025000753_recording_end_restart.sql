-- +goose Up
-- A cut short splits by cause (#853): a restart's keeps its meaning under
-- its own name, beside the new gap end. ended is plain TEXT, so a rename is
-- one UPDATE.
UPDATE recordings SET ended = 'restart' WHERE ended = 'cut_short';

-- +goose Down
-- The older release knows no gap end, and gives its own gap stop
-- recorder_left.
UPDATE recordings SET ended = 'cut_short' WHERE ended = 'restart';
UPDATE recordings SET ended = 'recorder_left' WHERE ended = 'gap';
