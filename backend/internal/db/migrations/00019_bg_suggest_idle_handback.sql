-- +goose Up
-- AI 建議回覆背景預先產生、真人閒置自動交回 AI、系統事件訊息。
--   - conversation_suggestions：每個對話一筆「最新」的建議（背景產生或手動「換一組」覆蓋）
--     for_message_id＝這份建議針對的那則顧客訊息；status ready｜pending（排程中／產生中）｜failed（error 為中文原因）
--     新顧客訊息進來時舊的直接刪掉；API 只在 for_message_id 等於「目前需要回覆的那則」時才回，過期的一律不回
--   - automation_settings.human_idle_minutes：handler=human 且最後一則未回覆的顧客訊息超過 N 分鐘 → 交回 AI（0＝關；只收 0/5/10/15/30/60）
--   - conversations.idle_handback_msg_id：已因閒置交回處理過的顧客訊息 id（同一則不重複交回；重啟後也成立）
--   - messages.sender_type='system'（kind='system'、direction='out'）：系統事件（不送 IG），例「真人 15 分鐘未回覆，已交回 AI」；
--     分析、摘要、蒸餾、回應時間一律排除
CREATE TABLE conversation_suggestions (
    conversation_id BIGINT PRIMARY KEY REFERENCES conversations(id) ON DELETE CASCADE,
    for_message_id  BIGINT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    status          TEXT NOT NULL CHECK (status IN ('ready', 'pending', 'failed')),
    options         JSONB NOT NULL DEFAULT '[]',
    warning         TEXT NOT NULL DEFAULT '',
    error           TEXT NOT NULL DEFAULT '',
    model           TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX conversation_suggestions_pending_idx ON conversation_suggestions (status) WHERE status = 'pending';

ALTER TABLE automation_settings ADD COLUMN human_idle_minutes INT NOT NULL DEFAULT 15
    CHECK (human_idle_minutes IN (0, 5, 10, 15, 30, 60));
ALTER TABLE conversations ADD COLUMN idle_handback_msg_id BIGINT;

-- +goose Down
-- 系統事件訊息只有新版看得懂（舊版會當成店家送出的訊息），一併移除；其餘資料保留
DELETE FROM messages WHERE sender_type = 'system';
ALTER TABLE conversations DROP COLUMN idle_handback_msg_id;
ALTER TABLE automation_settings DROP COLUMN human_idle_minutes;
DROP TABLE conversation_suggestions;
