-- +goose Up
ALTER TABLE hubs ADD COLUMN locking_allowed BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE spawned_channels ADD COLUMN locked                 BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE spawned_channels ADD COLUMN locker_user_id         TEXT;
ALTER TABLE spawned_channels ADD COLUMN lock_notice_message_id TEXT;

-- +goose Down
ALTER TABLE spawned_channels DROP COLUMN lock_notice_message_id;
ALTER TABLE spawned_channels DROP COLUMN locker_user_id;
ALTER TABLE spawned_channels DROP COLUMN locked;

ALTER TABLE hubs DROP COLUMN locking_allowed;
