-- +goose Up
-- 多管理員＋功能權限：店長（is_owner）擁有全部權限且不可被限制；其餘依 permissions 勾選
ALTER TABLE admin_users
    ADD COLUMN is_owner      BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN permissions   TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN disabled_at   TIMESTAMPTZ,
    ADD COLUMN last_login_at TIMESTAMPTZ;

-- 既有管理員（升級前只有 env 建的那位）一律視為店長
UPDATE admin_users SET is_owner = true;

-- 後台真人回覆記下是哪位管理員送的（自動回覆、從 IG app 直接回的為 NULL）
ALTER TABLE messages ADD COLUMN sent_by_admin_id BIGINT REFERENCES admin_users(id);

-- +goose Down
ALTER TABLE messages DROP COLUMN sent_by_admin_id;
ALTER TABLE admin_users
    DROP COLUMN last_login_at,
    DROP COLUMN disabled_at,
    DROP COLUMN permissions,
    DROP COLUMN is_owner;
