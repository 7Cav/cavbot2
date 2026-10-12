-- +goose Up
-- A removed hub keeps its change log (#579): a removal no longer clears the
-- hub reference of the hub's entries, and the removal's own entry
-- references the hub it removed, so the Hubs page can list a removed hub's
-- entries by its ID. The reference can name a hub row that no longer
-- exists. It can never name another hub, since hubs.id is a BIGSERIAL and
-- no ID is handed out twice.
ALTER TABLE change_log DROP CONSTRAINT change_log_hub_id_fkey;

-- +goose Down
-- The constraint holds only references to existing hubs, so the entries of
-- removed hubs go back under no hub first, as the older release left them.
UPDATE change_log SET hub_id = NULL
    WHERE hub_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM hubs WHERE hubs.id = change_log.hub_id);
ALTER TABLE change_log ADD CONSTRAINT change_log_hub_id_fkey
    FOREIGN KEY (hub_id) REFERENCES hubs (id) ON DELETE SET NULL;
