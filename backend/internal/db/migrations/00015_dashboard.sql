-- +goose Up
-- 總覽改版：
--   - automation_settings：每個帳號的自動回覆總開關（關閉＝訊息只收不回，不跑規則／AI／轉真人通知）、每日摘要產生時間（店家時區 HH:MM）
--   - daily_digests：每日私訊摘要（每帳號每天一份，重新產生＝覆蓋）
--   - webhook_events.received_at 索引：系統狀態每分鐘查各帳號最後收到 webhook 的時間
ALTER TABLE automation_settings
    ADD COLUMN auto_reply_enabled BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN digest_time        TEXT NOT NULL DEFAULT '09:00'
        CHECK (digest_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$');

CREATE TABLE daily_digests (
    id            BIGSERIAL PRIMARY KEY,
    ig_account_id BIGINT NOT NULL REFERENCES ig_accounts(id) ON DELETE CASCADE,
    digest_date   DATE NOT NULL,                       -- 摘要的是哪一天（店家時區）
    content       JSONB NOT NULL DEFAULT '{}',
    generated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (ig_account_id, digest_date)
);

CREATE INDEX webhook_events_received_idx ON webhook_events (received_at);

-- +goose Down
DROP INDEX webhook_events_received_idx;
DROP TABLE daily_digests;
ALTER TABLE automation_settings
    DROP COLUMN digest_time,
    DROP COLUMN auto_reply_enabled;
