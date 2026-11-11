-- +goose Up
-- 降頻（取代「大量訊息自動封鎖」）：顧客短時間傳太多則時不封鎖，改為「問題顧客（來源 rate）」＋降低自動回覆頻率。
--   throttle_level：0＝正常；N≥1＝每 5×2^(N-1) 分鐘最多自動回一次（上限 6 小時）。每回一次升一級；
--   顧客安靜超過目前間隔就歸零。上次自動回覆時間直接由 messages（direction=out、sender_type ai／bot）推得，不另存。
ALTER TABLE contacts ADD COLUMN throttle_level INT NOT NULL DEFAULT 0 CHECK (throttle_level >= 0);

-- +goose Down
ALTER TABLE contacts DROP COLUMN throttle_level;
