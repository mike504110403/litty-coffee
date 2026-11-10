-- +goose Up
CREATE TABLE tags (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    color       TEXT NOT NULL DEFAULT '#8b5e3c',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE contact_tags (
    contact_id  BIGINT NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    tag_id      BIGINT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (contact_id, tag_id)
);
CREATE INDEX contact_tags_tag_idx ON contact_tags (tag_id);

-- 分析查詢：依時間掃訊息、依意圖分組
CREATE INDEX messages_created_idx ON messages (created_at);
CREATE INDEX messages_intent_idx ON messages ((meta->>'intent')) WHERE direction = 'in';

-- +goose Down
DROP INDEX messages_intent_idx;
DROP INDEX messages_created_idx;
DROP TABLE contact_tags;
DROP TABLE tags;
