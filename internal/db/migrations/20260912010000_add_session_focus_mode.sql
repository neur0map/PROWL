-- +goose Up
ALTER TABLE sessions ADD COLUMN focus_mode TEXT NOT NULL DEFAULT '' CHECK (focus_mode IN ('', 'on', 'off'));

-- +goose Down
ALTER TABLE sessions DROP COLUMN focus_mode;
