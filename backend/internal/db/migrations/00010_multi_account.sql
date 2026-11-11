-- +goose Up
-- 改用 Instagram API with Unified Login（Facebook 登入＋業務組合系統用戶 token）並支援多個 IG 帳號：
--   - ig_connections：一次授權＝一個連線（業務組合的 system user access token，加密存放），底下可有多個 IG 帳號
--   - ig_accounts：掛到連線、可停用；舊的明文 token 欄位拿掉（既有帳號需重新連結，顧客與對話保留）
--   - 自動回覆（設定／知識庫／規則）、貼文、媒體依帳號區分；ig_account_id IS NULL 的自動回覆資料是「範本」，
--     新帳號第一次用到時從範本複製
--   - admin_users.ig_account_ids：可見帳號（NULL＝全部）

CREATE TABLE ig_connections (
    id                 BIGSERIAL PRIMARY KEY,
    client_business_id TEXT NOT NULL DEFAULT '',   -- 客戶的業務組合 ID（/me?fields=client_business_id）
    system_user_id     TEXT NOT NULL DEFAULT '',   -- token 所屬的系統用戶（/me 的 id）
    config_id          TEXT NOT NULL DEFAULT '',   -- 授權時用的 Facebook 登入配置 ID
    access_token       TEXT NOT NULL,              -- AES-256-GCM 密文（internal/secrets）
    token_expires      TIMESTAMPTZ,                -- NULL＝永不過期（配置預設）
    last_error         TEXT NOT NULL DEFAULT '',
    connected_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (client_business_id, system_user_id)
);

ALTER TABLE ig_accounts
    ADD COLUMN connection_id BIGINT REFERENCES ig_connections(id) ON DELETE SET NULL,
    ADD COLUMN active        BOOLEAN NOT NULL DEFAULT true;
CREATE INDEX ig_accounts_connection_idx ON ig_accounts (connection_id);

-- 自動回覆依帳號
ALTER TABLE knowledge_items ADD COLUMN ig_account_id BIGINT REFERENCES ig_accounts(id) ON DELETE CASCADE;
ALTER TABLE auto_rules ADD COLUMN ig_account_id BIGINT REFERENCES ig_accounts(id) ON DELETE CASCADE;
CREATE INDEX knowledge_items_account_idx ON knowledge_items (ig_account_id, sort);
CREATE INDEX auto_rules_account_idx ON auto_rules (ig_account_id, priority);

ALTER TABLE automation_settings DROP CONSTRAINT automation_settings_id_check;
CREATE SEQUENCE automation_settings_id_seq OWNED BY automation_settings.id;
SELECT setval('automation_settings_id_seq', GREATEST((SELECT max(id) FROM automation_settings), 1));
ALTER TABLE automation_settings ALTER COLUMN id SET DEFAULT nextval('automation_settings_id_seq');
ALTER TABLE automation_settings ADD COLUMN ig_account_id BIGINT UNIQUE REFERENCES ig_accounts(id) ON DELETE CASCADE;
-- 範本只能有一列
CREATE UNIQUE INDEX automation_settings_template_idx ON automation_settings ((true)) WHERE ig_account_id IS NULL;

ALTER TABLE scheduled_posts ADD COLUMN ig_account_id BIGINT REFERENCES ig_accounts(id);
ALTER TABLE media_assets ADD COLUMN ig_account_id BIGINT REFERENCES ig_accounts(id);

-- +goose StatementBegin
DO $$
DECLARE
    p BIGINT;        -- 主帳號：有 token 的那個，否則 id 最小的
    post_acc BIGINT; -- 貼文／媒體要掛的帳號（沒有帳號時建佔位帳號）
    t RECORD;
BEGIN
    SELECT id INTO p FROM ig_accounts WHERE access_token <> '' ORDER BY connected_at DESC NULLS LAST, id DESC LIMIT 1;
    IF p IS NULL THEN
        SELECT min(id) INTO p FROM ig_accounts;
    END IF;

    IF p IS NOT NULL THEN
        -- 既有資料（id 不變，messages.meta.ruleId 才不會斷）歸主帳號
        UPDATE knowledge_items SET ig_account_id = p;
        UPDATE auto_rules SET ig_account_id = p;
        UPDATE automation_settings SET ig_account_id = p;
        -- 其他帳號與範本（NULL）各一份副本；命中數歸零
        FOR t IN SELECT x.id FROM (SELECT id FROM ig_accounts WHERE id <> p UNION ALL SELECT NULL::bigint) x(id) LOOP
            INSERT INTO knowledge_items (title, content, enabled, sort, updated_at, ig_account_id)
            SELECT title, content, enabled, sort, updated_at, t.id FROM knowledge_items WHERE ig_account_id = p ORDER BY id;
            INSERT INTO auto_rules (name, match_type, keywords, reply, handoff, enabled, priority, hit_count, last_hit_at,
                                    created_at, updated_at, ig_account_id)
            SELECT name, match_type, keywords, reply, handoff, enabled, priority, 0, NULL, created_at, updated_at, t.id
            FROM auto_rules WHERE ig_account_id = p ORDER BY id;
            INSERT INTO automation_settings (ai_enabled, persona, handoff_message, debounce_secs, updated_at, ig_account_id)
            SELECT ai_enabled, persona, handoff_message, debounce_secs, updated_at, t.id
            FROM automation_settings WHERE ig_account_id = p;
        END LOOP;
    END IF;
    -- 沒有帳號時既有自動回覆資料維持 NULL＝範本；範本設定列一定要有
    IF NOT EXISTS (SELECT 1 FROM automation_settings WHERE ig_account_id IS NULL) THEN
        INSERT INTO automation_settings (ig_account_id) VALUES (NULL);
    END IF;

    post_acc := p;
    IF post_acc IS NULL AND (EXISTS (SELECT 1 FROM scheduled_posts) OR EXISTS (SELECT 1 FROM media_assets)) THEN
        INSERT INTO ig_accounts (ig_user_id, username, name, active)
        VALUES ('migrated-placeholder', '', '（升級前的貼文，請重新連結帳號）', false)
        RETURNING id INTO post_acc;
    END IF;
    IF post_acc IS NOT NULL THEN
        UPDATE scheduled_posts SET ig_account_id = post_acc;
        UPDATE media_assets m SET ig_account_id = COALESCE(
            (SELECT sp.ig_account_id FROM scheduled_posts sp WHERE m.id = ANY (sp.media_ids) ORDER BY sp.id LIMIT 1),
            post_acc);
    END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE scheduled_posts ALTER COLUMN ig_account_id SET NOT NULL;
ALTER TABLE media_assets ALTER COLUMN ig_account_id SET NOT NULL;
CREATE INDEX scheduled_posts_account_idx ON scheduled_posts (ig_account_id, scheduled_at);
CREATE INDEX media_assets_account_idx ON media_assets (ig_account_id);

-- 舊的單帳號 token（明文）不再使用：既有帳號需到設定頁重新連結
ALTER TABLE ig_accounts DROP COLUMN access_token, DROP COLUMN token_expires, DROP COLUMN token_refreshed_at;

-- 管理員可見的 IG 帳號（NULL 或空＝全部；店長一律全部）
ALTER TABLE admin_users ADD COLUMN ig_account_ids BIGINT[];

-- +goose Down
ALTER TABLE admin_users DROP COLUMN ig_account_ids;

ALTER TABLE ig_accounts
    ADD COLUMN access_token       TEXT NOT NULL DEFAULT '',
    ADD COLUMN token_expires      TIMESTAMPTZ,
    ADD COLUMN token_refreshed_at TIMESTAMPTZ;

DROP INDEX media_assets_account_idx;
DROP INDEX scheduled_posts_account_idx;
ALTER TABLE media_assets DROP COLUMN ig_account_id;
ALTER TABLE scheduled_posts DROP COLUMN ig_account_id;

-- 回到單一帳號：保留 id 最小帳號的自動回覆資料（沒有帳號資料時留範本）
-- +goose StatementBegin
DO $$
DECLARE
    keep BIGINT;
BEGIN
    SELECT min(ig_account_id) INTO keep FROM automation_settings;
    DELETE FROM knowledge_items WHERE ig_account_id IS DISTINCT FROM keep;
    DELETE FROM auto_rules WHERE ig_account_id IS DISTINCT FROM keep;
    DELETE FROM automation_settings WHERE ig_account_id IS DISTINCT FROM keep;
    UPDATE automation_settings SET id = 1;
    IF NOT EXISTS (SELECT 1 FROM automation_settings) THEN
        INSERT INTO automation_settings (id) VALUES (1);
    END IF;
END $$;
-- +goose StatementEnd
DROP INDEX automation_settings_template_idx;
ALTER TABLE automation_settings DROP COLUMN ig_account_id;
ALTER TABLE automation_settings ALTER COLUMN id SET DEFAULT 1;
DROP SEQUENCE automation_settings_id_seq;
ALTER TABLE automation_settings ADD CONSTRAINT automation_settings_id_check CHECK (id = 1);
DROP INDEX auto_rules_account_idx;
DROP INDEX knowledge_items_account_idx;
ALTER TABLE auto_rules DROP COLUMN ig_account_id;
ALTER TABLE knowledge_items DROP COLUMN ig_account_id;

-- 佔位帳號（升級時沒有帳號卻有貼文才會建）底下沒有顧客就拿掉
DELETE FROM ig_accounts a WHERE a.ig_user_id = 'migrated-placeholder'
  AND NOT EXISTS (SELECT 1 FROM contacts c WHERE c.ig_account_id = a.id);

DROP INDEX ig_accounts_connection_idx;
ALTER TABLE ig_accounts DROP COLUMN connection_id, DROP COLUMN active;
DROP TABLE ig_connections;
