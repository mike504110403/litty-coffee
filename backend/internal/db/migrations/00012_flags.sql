-- +goose Up
-- 標記與黑名單：
--   - contacts.risk：'' 一般｜watch 問題顧客（AI 照常回）｜blocked 黑名單（訊息照收，但不跑規則／AI、不發轉真人通知）
--     risk_source：manual 管理員｜rate 短時間大量訊息（只會標 watch＋降頻，見 00013）｜ai AI 判斷騷擾；解除時 risk='' 但保留誰、何時解除（risk_by_admin_id、risk_at）
--   - message_reviews：待改進問答。ai_wrong＝管理員標「回答有誤」（掛在我方自動回覆那則）；
--     ai_unsure＝AI 轉真人或拒答時自動記錄（掛在顧客那則）
--   - automation_settings：大量訊息門檻（spam_max_msgs 則／spam_window_secs 秒，0 則＝關閉）、AI 判斷騷擾開關

ALTER TABLE contacts
    ADD COLUMN risk             TEXT NOT NULL DEFAULT '' CHECK (risk IN ('', 'watch', 'blocked')),
    ADD COLUMN risk_reason      TEXT NOT NULL DEFAULT '',
    ADD COLUMN risk_source      TEXT NOT NULL DEFAULT '' CHECK (risk_source IN ('', 'manual', 'rate', 'ai')),
    ADD COLUMN risk_at          TIMESTAMPTZ,
    ADD COLUMN risk_by_admin_id BIGINT REFERENCES admin_users(id) ON DELETE SET NULL;
CREATE INDEX contacts_risk_idx ON contacts (risk) WHERE risk <> '';

CREATE TABLE message_reviews (
    id                   BIGSERIAL PRIMARY KEY,
    message_id           BIGINT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    ig_account_id        BIGINT NOT NULL REFERENCES ig_accounts(id) ON DELETE CASCADE,
    kind                 TEXT NOT NULL CHECK (kind IN ('ai_wrong', 'ai_unsure')),
    note                 TEXT NOT NULL DEFAULT '',
    created_by_admin_id  BIGINT REFERENCES admin_users(id) ON DELETE SET NULL,   -- ai_unsure 為 NULL（系統自動）
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at          TIMESTAMPTZ,
    resolved_by_admin_id BIGINT REFERENCES admin_users(id) ON DELETE SET NULL
);
CREATE INDEX message_reviews_account_idx ON message_reviews (ig_account_id, created_at DESC);
-- 同一則訊息同一種標記，未處理的只留一筆
CREATE UNIQUE INDEX message_reviews_open_uniq ON message_reviews (message_id, kind) WHERE resolved_at IS NULL;

ALTER TABLE automation_settings
    ADD COLUMN spam_max_msgs    INT NOT NULL DEFAULT 10,
    ADD COLUMN spam_window_secs INT NOT NULL DEFAULT 300,
    ADD COLUMN ai_moderation    BOOLEAN NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE automation_settings
    DROP COLUMN spam_max_msgs,
    DROP COLUMN spam_window_secs,
    DROP COLUMN ai_moderation;
DROP TABLE message_reviews;
DROP INDEX contacts_risk_idx;
ALTER TABLE contacts
    DROP COLUMN risk,
    DROP COLUMN risk_reason,
    DROP COLUMN risk_source,
    DROP COLUMN risk_at,
    DROP COLUMN risk_by_admin_id;
