-- +goose Up
-- 超級管理員＋Google Authenticator（TOTP）兩步驟登入。
--   is_super：位階在店長之上；IG 帳號連結與串接憑證只限超管。
--   must_change_password：登入後只能改密碼／綁 GA／登出，改完才清。
--   totp_secret：以 internal/secrets 加密（AAD 綁管理員 id）；totp_enabled_at NULL＝尚未綁定（有 secret＝綁定中）。
--   totp_last_step：最後用過的時間步，同一組驗證碼不能重用。
--   totp_failures／totp_failures_since：驗證碼錯誤計數（固定 5 分鐘視窗）。
--   mfa_pending_id：密碼正確後發的待驗證碼 token（5 分鐘、一次性）；換到正式 session 即清。
ALTER TABLE admin_users
    ADD COLUMN is_super              BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN must_change_password  BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN totp_secret           TEXT,
    ADD COLUMN totp_enabled_at       TIMESTAMPTZ,
    ADD COLUMN totp_last_step        BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN totp_failures         INT NOT NULL DEFAULT 0,
    ADD COLUMN totp_failures_since   TIMESTAMPTZ,
    ADD COLUMN mfa_pending_id        TEXT;

-- 備援碼：只存 SHA-256；used_at 非 NULL＝已用過（一次性）。重新綁定或重設 GA 時整批刪掉。
CREATE TABLE admin_recovery_codes (
    id         BIGSERIAL PRIMARY KEY,
    admin_id   BIGINT NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    code_hash  TEXT NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX admin_recovery_codes_admin_idx ON admin_recovery_codes (admin_id);

-- IG 帳號、串接憑證改為超管專屬：不再是可授予的權限，既有勾選一併拿掉
UPDATE admin_users SET permissions = array_remove(array_remove(permissions, 'ig_accounts'), 'credentials')
WHERE permissions && ARRAY['ig_accounts', 'credentials'];

-- 預設超管（Mike 2026-11-12 裁示：repo 公開、密碼在 repo 內，接受風險；靠首次登入強制改密碼＋綁 GA 緩解）
-- 密碼 admin123 的 bcrypt 雜湊
INSERT INTO admin_users (username, password_hash, display_name, is_super, must_change_password)
VALUES ('superAdmin', '$2a$10$B/ldziZKMpsJhJ8pj4mZYepZOeCJ17L4nLlImL.rct6HhLYmLr3DG', '超級管理員', true, true)
ON CONFLICT (username) DO NOTHING;

-- +goose Down
-- 被拿掉的 ig_accounts／credentials 勾選無法還原（Down 後要由店長重新勾）。
-- 從沒登入過的預設超管直接刪掉（再 Up 會重新建立）；其他超管不刪（可能已有操作紀錄、回覆者等關聯），
-- 但拿掉身分後會變成可見全部帳號的一般管理員，所以一律停用並登出。
DELETE FROM admin_users WHERE username = 'superAdmin' AND is_super AND must_change_password AND last_login_at IS NULL;
UPDATE admin_users SET disabled_at = COALESCE(disabled_at, now()), session_id = NULL WHERE is_super;
DROP TABLE admin_recovery_codes;
ALTER TABLE admin_users
    DROP COLUMN mfa_pending_id,
    DROP COLUMN totp_failures_since,
    DROP COLUMN totp_failures,
    DROP COLUMN totp_last_step,
    DROP COLUMN totp_enabled_at,
    DROP COLUMN totp_secret,
    DROP COLUMN must_change_password,
    DROP COLUMN is_super;
