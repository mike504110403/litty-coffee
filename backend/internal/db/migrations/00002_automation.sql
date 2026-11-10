-- +goose Up
-- 店家知識庫：AI 回答的唯一依據
CREATE TABLE knowledge_items (
    id          BIGSERIAL PRIMARY KEY,
    title       TEXT NOT NULL,
    content     TEXT NOT NULL DEFAULT '',
    enabled     BOOLEAN NOT NULL DEFAULT true,
    sort        INT NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO knowledge_items (title, sort) VALUES
  ('營業時間', 10), ('地址與交通', 20), ('菜單與價格', 30), ('訂位方式', 40), ('常見問題', 50);

-- 關鍵字規則：比 AI 先跑，命中即用固定回覆
CREATE TABLE auto_rules (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL,
    match_type  TEXT NOT NULL DEFAULT 'contains',   -- exact | contains | regex
    keywords    TEXT[] NOT NULL DEFAULT '{}',
    reply       TEXT NOT NULL DEFAULT '',
    handoff     BOOLEAN NOT NULL DEFAULT false,      -- 命中後轉真人
    enabled     BOOLEAN NOT NULL DEFAULT true,
    priority    INT NOT NULL DEFAULT 100,            -- 小的先比
    hit_count   BIGINT NOT NULL DEFAULT 0,
    last_hit_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 自動回覆設定（單列）
CREATE TABLE automation_settings (
    id              INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    ai_enabled      BOOLEAN NOT NULL DEFAULT true,
    persona         TEXT NOT NULL DEFAULT '你是 Litty Coffee 的小幫手，語氣溫暖親切、簡短，像店員在 IG 回訊息。可以適度用一個 emoji。',
    handoff_message TEXT NOT NULL DEFAULT '收到～這個問題我請店員親自回覆你，請稍等一下喔！',
    debounce_secs   INT NOT NULL DEFAULT 4,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO automation_settings (id) VALUES (1);

-- +goose Down
DROP TABLE automation_settings;
DROP TABLE auto_rules;
DROP TABLE knowledge_items;
