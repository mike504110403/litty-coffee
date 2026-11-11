-- +goose Up
-- 後台可改的執行期設定（IG Meta App 憑證、Claude API key 等）。
-- secret 類的 value 是 AES-256-GCM 密文（見 internal/secrets），非 secret 為明文；DB 值優先於 .env。
CREATE TABLE app_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE app_settings;
