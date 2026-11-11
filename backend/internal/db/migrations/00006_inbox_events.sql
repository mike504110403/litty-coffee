-- +goose Up
-- 收件匣：附件存到自有儲存（IG 的 CDN 網址會過期）、顧客的表情回應、收回、已讀
ALTER TABLE messages
    ADD COLUMN media_file      TEXT NOT NULL DEFAULT '',  -- MEDIA_DIR/inbox/ 下的檔名；空＝尚未下載或下載失敗
    ADD COLUMN media_error     TEXT NOT NULL DEFAULT '',
    ADD COLUMN reaction        TEXT NOT NULL DEFAULT '',  -- 顧客對這則訊息的表情（emoji）
    ADD COLUMN deleted_at      TIMESTAMPTZ;               -- 顧客收回
ALTER TABLE conversations
    ADD COLUMN contact_read_at TIMESTAMPTZ;               -- 顧客最後一次已讀的時間

-- +goose Down
ALTER TABLE conversations DROP COLUMN contact_read_at;
ALTER TABLE messages DROP COLUMN media_file, DROP COLUMN media_error, DROP COLUMN reaction, DROP COLUMN deleted_at;
