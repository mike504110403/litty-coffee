-- +goose Up
-- Unified Login 的 /me/assigned_instagram_accounts 可能列不出已分享的帳號（實測：token 能讀該帳號，但清單是空的），
-- 所以允許店長手動輸入 IG 帳號編號加入連線；手動加入的帳號「同步帳號」時不會被拿掉。
ALTER TABLE ig_accounts ADD COLUMN added_manually BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE ig_accounts DROP COLUMN added_manually;
