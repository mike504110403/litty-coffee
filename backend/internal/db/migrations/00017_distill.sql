-- +goose Up
-- 蒸餾自動回覆：用 Claude 把既有對話整理成「建議的」關鍵字規則與知識庫條目，店家在後台逐筆採用／編輯後採用／略過，不會自動生效。
--   - distill_runs：每次蒸餾（手動或每週自動）；status running｜done｜failed｜empty（期間內沒有素材，沒呼叫 AI）
--     同一帳號同時只會有一筆 running（部分唯一索引；行程中斷留下的 running 由程式在下次啟動蒸餾時標 failed）
--   - distill_suggestions：建議本身（payload jsonb：type rule｜knowledge、action create｜update、targetId、內容、理由、節錄、信心）
--     status pending｜accepted｜dismissed；新一次蒸餾完成時，舊的 pending 由系統略過（decided_by NULL）
--     result_id＝採用後新增／修改的規則或知識庫 id
--   - automation_settings.distill_weekly：每週一 09:00（店家時區）自動蒸餾近 7 天，預設關
CREATE TABLE distill_runs (
    id                  BIGSERIAL PRIMARY KEY,
    ig_account_id       BIGINT NOT NULL REFERENCES ig_accounts(id) ON DELETE CASCADE,
    days                INT NOT NULL CHECK (days IN (7, 30, 90)),
    trigger             TEXT NOT NULL DEFAULT 'manual' CHECK (trigger IN ('manual', 'weekly')),
    status              TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'done', 'failed', 'empty')),
    error               TEXT NOT NULL DEFAULT '',
    pair_count          INT NOT NULL DEFAULT 0,   -- 送進去的「顧客問→真人答」組數
    review_count        INT NOT NULL DEFAULT 0,   -- 送進去的待改進問答筆數
    input_chars         INT NOT NULL DEFAULT 0,   -- 素材字數
    suggestion_count    INT NOT NULL DEFAULT 0,
    created_by_admin_id BIGINT REFERENCES admin_users(id) ON DELETE SET NULL,   -- 每週自動為 NULL
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at         TIMESTAMPTZ
);
CREATE INDEX distill_runs_account_idx ON distill_runs (ig_account_id, created_at DESC);
CREATE UNIQUE INDEX distill_runs_running_uniq ON distill_runs (ig_account_id) WHERE status = 'running';

CREATE TABLE distill_suggestions (
    id                  BIGSERIAL PRIMARY KEY,
    run_id              BIGINT NOT NULL REFERENCES distill_runs(id) ON DELETE CASCADE,
    ig_account_id       BIGINT NOT NULL REFERENCES ig_accounts(id) ON DELETE CASCADE,
    payload             JSONB NOT NULL,
    status              TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'dismissed')),
    result_id           BIGINT,
    decided_by_admin_id BIGINT REFERENCES admin_users(id) ON DELETE SET NULL,
    decided_at          TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX distill_suggestions_account_idx ON distill_suggestions (ig_account_id, status, id DESC);
CREATE INDEX distill_suggestions_run_idx ON distill_suggestions (run_id);

ALTER TABLE automation_settings ADD COLUMN distill_weekly BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE automation_settings DROP COLUMN distill_weekly;
DROP TABLE distill_suggestions;
DROP TABLE distill_runs;
