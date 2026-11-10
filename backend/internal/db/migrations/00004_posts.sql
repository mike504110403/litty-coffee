-- +goose Up
-- 上傳的媒體檔（IG 發佈時要能從公開網址抓到）
CREATE TABLE media_assets (
    id            BIGSERIAL PRIMARY KEY,
    filename      TEXT NOT NULL UNIQUE,      -- 存在 MEDIA_DIR 下的檔名
    original_name TEXT NOT NULL DEFAULT '',
    content_type  TEXT NOT NULL,
    kind          TEXT NOT NULL,             -- image | video
    size_bytes    BIGINT NOT NULL,
    width         INT NOT NULL DEFAULT 0,
    height        INT NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE scheduled_posts (
    id              BIGSERIAL PRIMARY KEY,
    kind            TEXT NOT NULL DEFAULT 'image',     -- image | carousel | reel | story
    caption         TEXT NOT NULL DEFAULT '',
    media_ids       BIGINT[] NOT NULL DEFAULT '{}',    -- 依顯示順序
    scheduled_at    TIMESTAMPTZ,
    status          TEXT NOT NULL DEFAULT 'draft',     -- draft | scheduled | publishing | published | failed
    attempts        INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ,                       -- 自動重試時間
    ig_container_id TEXT NOT NULL DEFAULT '',          -- 中斷復原用：查容器是否已發佈
    ig_media_id     TEXT NOT NULL DEFAULT '',
    permalink       TEXT NOT NULL DEFAULT '',
    error           TEXT NOT NULL DEFAULT '',
    published_at    TIMESTAMPTZ,
    locked_at       TIMESTAMPTZ,                       -- 進入 publishing 的時間
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX scheduled_posts_due_idx ON scheduled_posts (next_attempt_at) WHERE status = 'scheduled';
CREATE INDEX scheduled_posts_when_idx ON scheduled_posts (scheduled_at);

-- +goose Down
DROP TABLE scheduled_posts;
DROP TABLE media_assets;
