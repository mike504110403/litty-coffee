-- +goose Up
-- 已發佈貼文的成效快照（IG Insights）：只留最新一份
ALTER TABLE scheduled_posts
    ADD COLUMN insights            JSONB NOT NULL DEFAULT '{}',  -- {"views":n,"reach":n,"likes":n,...}，依貼文類型而異
    ADD COLUMN insights_fetched_at TIMESTAMPTZ,                  -- 最近一次成功取得
    ADD COLUMN insights_checked_at TIMESTAMPTZ,                  -- 最近一次嘗試（成功或失敗），排程節奏依此
    ADD COLUMN insights_error      TEXT NOT NULL DEFAULT '';     -- 最近一次失敗原因，成功時清空
CREATE INDEX scheduled_posts_published_idx ON scheduled_posts (published_at) WHERE status = 'published';

-- +goose Down
DROP INDEX scheduled_posts_published_idx;
ALTER TABLE scheduled_posts
    DROP COLUMN insights, DROP COLUMN insights_fetched_at, DROP COLUMN insights_checked_at, DROP COLUMN insights_error;
