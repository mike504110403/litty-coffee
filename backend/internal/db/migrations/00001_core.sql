-- +goose Up
CREATE TABLE admin_users (
    id            BIGSERIAL PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    display_name  TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 連結的 IG 專業帳號（MVP 只會有一個，但模型先留多帳號）
CREATE TABLE ig_accounts (
    id            BIGSERIAL PRIMARY KEY,
    ig_user_id    TEXT NOT NULL UNIQUE,
    username      TEXT NOT NULL DEFAULT '',
    access_token  TEXT NOT NULL DEFAULT '',
    token_expires TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 傳訊給我們的 IG 用戶（IGSID 以帳號為範圍）
CREATE TABLE contacts (
    id             BIGSERIAL PRIMARY KEY,
    ig_account_id  BIGINT NOT NULL REFERENCES ig_accounts(id),
    igsid          TEXT NOT NULL,
    username       TEXT NOT NULL DEFAULT '',
    name           TEXT NOT NULL DEFAULT '',
    avatar_url     TEXT NOT NULL DEFAULT '',
    note           TEXT NOT NULL DEFAULT '',
    first_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (ig_account_id, igsid)
);

CREATE TABLE conversations (
    id                 BIGSERIAL PRIMARY KEY,
    contact_id         BIGINT NOT NULL UNIQUE REFERENCES contacts(id),
    status             TEXT NOT NULL DEFAULT 'open',      -- open | closed
    handler            TEXT NOT NULL DEFAULT 'bot',       -- bot | human（human＝bot 暫停）
    unread_count       INT NOT NULL DEFAULT 0,
    last_message_at    TIMESTAMPTZ,
    last_inbound_at    TIMESTAMPTZ,                       -- 24h 回覆窗口以此起算
    last_message_text  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX conversations_last_message_idx ON conversations (last_message_at DESC);

CREATE TABLE messages (
    id               BIGSERIAL PRIMARY KEY,
    conversation_id  BIGINT NOT NULL REFERENCES conversations(id),
    ig_mid           TEXT UNIQUE,                         -- IG message id，冪等鍵
    direction        TEXT NOT NULL,                       -- in | out
    sender_type      TEXT NOT NULL,                       -- contact | bot | ai | human
    kind             TEXT NOT NULL DEFAULT 'text',        -- text | image | video | story_reply | ...
    text             TEXT NOT NULL DEFAULT '',
    media_url        TEXT NOT NULL DEFAULT '',
    meta             JSONB NOT NULL DEFAULT '{}',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX messages_conversation_idx ON messages (conversation_id, created_at);

-- 原始 webhook 事件，先落庫再處理（可重播）
CREATE TABLE webhook_events (
    id            BIGSERIAL PRIMARY KEY,
    source        TEXT NOT NULL DEFAULT 'instagram',
    payload       JSONB NOT NULL,
    processed_at  TIMESTAMPTZ,
    error         TEXT NOT NULL DEFAULT '',
    received_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX webhook_events_pending_idx ON webhook_events (id) WHERE processed_at IS NULL;

-- +goose Down
DROP TABLE webhook_events;
DROP TABLE messages;
DROP TABLE conversations;
DROP TABLE contacts;
DROP TABLE ig_accounts;
DROP TABLE admin_users;
