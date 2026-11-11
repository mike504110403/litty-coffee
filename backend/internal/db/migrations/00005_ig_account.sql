-- +goose Up
-- 設定頁的 IG 帳號連結（OAuth）：token 改存 DB、背景自動續期
ALTER TABLE ig_accounts
    ADD COLUMN name               TEXT NOT NULL DEFAULT '',
    ADD COLUMN profile_pic        TEXT NOT NULL DEFAULT '',
    ADD COLUMN account_type       TEXT NOT NULL DEFAULT '',
    ADD COLUMN connected_at       TIMESTAMPTZ,
    ADD COLUMN token_refreshed_at TIMESTAMPTZ,
    ADD COLUMN webhook_subscribed BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN last_error         TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE ig_accounts
    DROP COLUMN name, DROP COLUMN profile_pic, DROP COLUMN account_type, DROP COLUMN connected_at,
    DROP COLUMN token_refreshed_at, DROP COLUMN webhook_subscribed, DROP COLUMN last_error;
