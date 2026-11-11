-- +goose Up
-- IG 貼文同步：帳號上「不是從後台發的」貼文、輪播、Reels、限動也同步進 scheduled_posts，看得到成效、當 AI 參考資料。
--   - source：app＝後台發的；ig＝從 IG 同步進來的（API 唯讀，只能切 ai_exclude 與手動更新成效）
--   - ig_thumb：同步貼文的縮圖檔名（下載到 MEDIA_DIR，經 /media/{檔名} 提供；IG CDN 網址會過期，不存網址）
--   - ig_synced_at：最近一次由同步寫入／更新的時間（後台發的貼文被同步到時也會寫）
--   - ai_exclude：單篇不給 AI 參考
--   - 同帳號同一個 IG media 只能一筆（部分唯一索引；ig_media_id 只在發佈成功時寫入，既有資料一篇一個 media id）
--   - automation_settings.posts_kb_enabled：AI（自動回覆、建議回覆、寫文案、蒸餾）是否參考近期 IG 貼文，預設開
ALTER TABLE scheduled_posts
    ADD COLUMN source       TEXT NOT NULL DEFAULT 'app' CHECK (source IN ('app', 'ig')),
    ADD COLUMN ig_thumb     TEXT NOT NULL DEFAULT '',
    ADD COLUMN ig_synced_at TIMESTAMPTZ,
    ADD COLUMN ai_exclude   BOOLEAN NOT NULL DEFAULT false;
CREATE UNIQUE INDEX scheduled_posts_ig_media_uniq ON scheduled_posts (ig_account_id, ig_media_id) WHERE ig_media_id <> '';

ALTER TABLE automation_settings ADD COLUMN posts_kb_enabled BOOLEAN NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE automation_settings DROP COLUMN posts_kb_enabled;
DROP INDEX scheduled_posts_ig_media_uniq;
-- 同步進來的貼文保留（舊版視為已發佈貼文：不能改，只能刪本地紀錄）
ALTER TABLE scheduled_posts
    DROP COLUMN source, DROP COLUMN ig_thumb, DROP COLUMN ig_synced_at, DROP COLUMN ai_exclude;
