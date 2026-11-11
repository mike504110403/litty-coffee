-- +goose Up
-- 單一登入：每次登入產生新的 session_id 寫進 JWT（sid）；請求帶的 sid 與這裡不符＝已在別處登入（舊的被踢）。
--   NULL＝沒有有效 session（登出、被停用、被重設密碼）。last_seen_at 最多每分鐘更新一次，供管理員清單判斷在線。
ALTER TABLE admin_users
    ADD COLUMN session_id   TEXT,
    ADD COLUMN last_seen_at TIMESTAMPTZ;

-- 操作紀錄：登入／登出／被踢／所有登入後的異動請求（POST/PUT/PATCH/DELETE）／IG 連結結果。背景自動動作不記。
--   admin_id 可為 NULL（登入失敗找不到帳號）；admin_name 是當下快照。ig_account_id 不設 FK：帳號刪掉紀錄照留。
--   保留 365 天（背景每日清）。
CREATE TABLE audit_logs (
    id            BIGSERIAL PRIMARY KEY,
    admin_id      BIGINT REFERENCES admin_users(id) ON DELETE SET NULL,
    admin_name    TEXT NOT NULL DEFAULT '',
    action        TEXT NOT NULL,
    summary       TEXT NOT NULL DEFAULT '',
    target_type   TEXT NOT NULL DEFAULT '',
    target_id     TEXT NOT NULL DEFAULT '',
    ig_account_id BIGINT,
    detail        JSONB NOT NULL DEFAULT '{}',
    ip            TEXT NOT NULL DEFAULT '',
    user_agent    TEXT NOT NULL DEFAULT '',
    status        INT NOT NULL DEFAULT 0,
    success       BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX audit_logs_created_idx ON audit_logs (created_at DESC, id DESC);
CREATE INDEX audit_logs_admin_idx ON audit_logs (admin_id, created_at DESC);
CREATE INDEX audit_logs_action_idx ON audit_logs (action, created_at DESC);
CREATE INDEX audit_logs_account_idx ON audit_logs (ig_account_id, created_at DESC);
-- 被踢只記一次：同一個舊 session（雜湊）只留一筆
CREATE UNIQUE INDEX audit_logs_kicked_once_idx ON audit_logs ((detail->>'session')) WHERE action = 'auth.kicked';

-- +goose Down
DROP TABLE audit_logs;
ALTER TABLE admin_users
    DROP COLUMN last_seen_at,
    DROP COLUMN session_id;
