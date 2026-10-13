-- +goose Up
-- An approval names a Foxhole role (#662). approved holds the External
-- approval, the only kind there was before, so every approval stored before
-- reads as External, and approved_internal holds the Internal one.
--
-- The previous release runs on it as before, and reads an Internal approval
-- as none. A rollback changes one thing: that release's note save and
-- approval clear drop a record left with no note and no External approval,
-- an Internal approval with it.
ALTER TABLE foxhole_records ADD COLUMN approved_internal BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
-- A record holds a note or an approval. One whose only approval was
-- Internal goes before the column does, as the older release keeps none.
DELETE FROM foxhole_records WHERE note = '' AND NOT approved;
ALTER TABLE foxhole_records DROP COLUMN approved_internal;
