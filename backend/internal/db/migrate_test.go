package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"golang.org/x/crypto/bcrypt"
)

// isolated 在測試庫裡開一個獨立 schema（search_path），讓 migration 從零跑，不碰其他測試的表。
func isolated(t *testing.T) *sql.DB {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	schema := fmt.Sprintf("migtest_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) })
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	d, err := sql.Open("pgx", base+sep+"search_path="+schema)
	if err != nil {
		t.Fatal(err)
	}
	d.SetMaxOpenConns(1)
	t.Cleanup(func() { d.Close() })
	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	return d
}

func mustExec(t *testing.T, d *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func intOf(t *testing.T, d *sql.DB, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := d.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func strOf(t *testing.T, d *sql.DB, q string, args ...any) string {
	t.Helper()
	var s string
	if err := d.QueryRow(q, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return s
}

// 00010：既有（單帳號時代的）資料升級後全部保留，自動回覆資料 id 不變並歸主帳號，其他帳號與範本各一份副本；
// 再 Down 回單帳號、再 Up 也要能跑。
func TestMigration10PreservesData(t *testing.T) {
	d := isolated(t)
	ctx := context.Background()
	if err := goose.UpToContext(ctx, d, "migrations", 9); err != nil {
		t.Fatal(err)
	}
	// 升級前：兩個帳號，B（id 2）有 token＝主帳號；A（id 1）是舊的、沒 token 但有顧客
	mustExec(t, d, `INSERT INTO ig_accounts (ig_user_id, username) VALUES ('old-a', 'a')`)
	mustExec(t, d, `INSERT INTO ig_accounts (ig_user_id, username, access_token, token_expires, connected_at)
VALUES ('main-b', 'b', 'plain-token', now() + interval '50 days', now())`)
	mustExec(t, d, `INSERT INTO contacts (ig_account_id, igsid, username) VALUES (1, 'u1', 'alice'), (2, 'u2', 'bob')`)
	mustExec(t, d, `INSERT INTO conversations (contact_id) VALUES (1), (2)`)
	mustExec(t, d, `INSERT INTO auto_rules (name, keywords, reply, hit_count, last_hit_at) VALUES
('菜單', '{菜單}', '在限動', 7, now()), ('營業', '{幾點}', '10點', 3, now())`)
	mustExec(t, d, `INSERT INTO messages (conversation_id, ig_mid, direction, sender_type, text, meta) VALUES
(2, 'm1', 'out', 'bot', '在限動', '{"ruleId": 1, "ruleName": "菜單"}')`)
	mustExec(t, d, `UPDATE knowledge_items SET content = '週一公休' WHERE title = '營業時間'`)
	mustExec(t, d, `UPDATE automation_settings SET persona = '舊人設', debounce_secs = 9`)
	mustExec(t, d, `INSERT INTO media_assets (filename, content_type, kind, size_bytes) VALUES ('a.jpg', 'image/jpeg', 'image', 1), ('orphan.jpg', 'image/jpeg', 'image', 1)`)
	mustExec(t, d, `INSERT INTO scheduled_posts (kind, caption, media_ids, status) VALUES ('image', '舊貼文', '{1}', 'draft')`)
	mustExec(t, d, `INSERT INTO admin_users (username, password_hash, is_owner) VALUES ('boss', 'x', true)`)
	kbBefore := intOf(t, d, `SELECT count(*) FROM knowledge_items`)

	if err := goose.UpToContext(ctx, d, "migrations", 10); err != nil {
		t.Fatal(err)
	}
	// 帳號、顧客、對話、訊息原封不動；舊 token 欄位拿掉、帳號需重新連結
	if intOf(t, d, `SELECT count(*) FROM ig_accounts`) != 2 || intOf(t, d, `SELECT count(*) FROM contacts`) != 2 ||
		intOf(t, d, `SELECT count(*) FROM conversations`) != 2 || intOf(t, d, `SELECT count(*) FROM messages`) != 1 {
		t.Fatal("核心資料數量變了")
	}
	if intOf(t, d, `SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema()
  AND table_name = 'ig_accounts' AND column_name IN ('access_token', 'token_expires', 'token_refreshed_at')`) != 0 {
		t.Fatal("ig_accounts 舊 token 欄位應移除")
	}
	if intOf(t, d, `SELECT count(*) FROM ig_accounts WHERE connection_id IS NULL AND active`) != 2 {
		t.Fatal("既有帳號應為啟用、未掛連線（需重新連結）")
	}
	// 原規則 id 不變、歸主帳號（B＝2）、命中數保留；meta.ruleId 指得到
	if intOf(t, d, `SELECT ig_account_id FROM auto_rules WHERE id = 1`) != 2 || intOf(t, d, `SELECT hit_count FROM auto_rules WHERE id = 1`) != 7 {
		t.Fatal("原規則應歸主帳號並保留命中數")
	}
	if strOf(t, d, `SELECT r.name FROM messages m JOIN auto_rules r ON r.id = (m.meta->>'ruleId')::bigint WHERE m.ig_mid = 'm1'`) != "菜單" {
		t.Fatal("messages.meta.ruleId 應仍指向原規則")
	}
	// A 與範本各一份副本，命中數歸零
	for _, cond := range []string{"ig_account_id = 1", "ig_account_id IS NULL"} {
		if intOf(t, d, `SELECT count(*) FROM auto_rules WHERE `+cond) != 2 || intOf(t, d, `SELECT COALESCE(sum(hit_count), 0) FROM auto_rules WHERE `+cond) != 0 {
			t.Fatalf("rules copy (%s)", cond)
		}
		if intOf(t, d, `SELECT count(*) FROM knowledge_items WHERE `+cond) != kbBefore ||
			strOf(t, d, `SELECT content FROM knowledge_items WHERE title = '營業時間' AND `+cond) != "週一公休" {
			t.Fatalf("knowledge copy (%s)", cond)
		}
		if strOf(t, d, `SELECT persona FROM automation_settings WHERE `+cond) != "舊人設" {
			t.Fatalf("settings copy (%s)", cond)
		}
	}
	if intOf(t, d, `SELECT id FROM automation_settings WHERE ig_account_id = 2`) != 1 {
		t.Fatal("原設定列（id 1）應歸主帳號")
	}
	// 新帳號可以再加一列設定（CHECK id=1 已拿掉）、範本只能一列
	mustExec(t, d, `INSERT INTO ig_accounts (ig_user_id) VALUES ('new-c')`)
	mustExec(t, d, `INSERT INTO automation_settings (ig_account_id) VALUES (3)`)
	if _, err := d.Exec(`INSERT INTO automation_settings (ig_account_id) VALUES (NULL)`); err == nil {
		t.Fatal("範本設定只能一列")
	}
	// 貼文與媒體歸主帳號；管理員可見範圍預設全部
	if intOf(t, d, `SELECT ig_account_id FROM scheduled_posts`) != 2 || intOf(t, d, `SELECT count(*) FROM media_assets WHERE ig_account_id = 2`) != 2 {
		t.Fatal("貼文／媒體應歸主帳號")
	}
	if intOf(t, d, `SELECT count(*) FROM admin_users WHERE ig_account_ids IS NULL`) != 1 {
		t.Fatal("管理員預設可見全部帳號")
	}

	// Down：回到單帳號，保留 id 最小帳號（A＝1）的自動回覆資料
	if err := goose.DownToContext(ctx, d, "migrations", 9); err != nil {
		t.Fatal(err)
	}
	if intOf(t, d, `SELECT count(*) FROM automation_settings`) != 1 || intOf(t, d, `SELECT id FROM automation_settings`) != 1 {
		t.Fatal("down: 設定應只剩 id=1 一列")
	}
	if intOf(t, d, `SELECT count(*) FROM auto_rules`) != 2 || intOf(t, d, `SELECT count(*) FROM knowledge_items`) != kbBefore {
		t.Fatal("down: 應只剩一個帳號的規則與知識庫")
	}
	if _, err := d.Exec(`INSERT INTO automation_settings (id) VALUES (2)`); err == nil {
		t.Fatal("down: CHECK (id = 1) 應恢復")
	}
	if intOf(t, d, `SELECT count(*) FROM contacts`) != 2 || intOf(t, d, `SELECT count(*) FROM scheduled_posts`) != 1 {
		t.Fatal("down: 顧客／貼文應保留")
	}
	mustExec(t, d, `SELECT access_token, token_expires, token_refreshed_at FROM ig_accounts`)
	// 再 Up 一次也要能跑
	if err := goose.UpContext(ctx, d, "migrations"); err != nil {
		t.Fatal(err)
	}
}

// 沒有任何帳號但有貼文：建一個停用的佔位帳號承接貼文與媒體；自動回覆資料成為範本。
func TestMigration10PlaceholderAccount(t *testing.T) {
	d := isolated(t)
	ctx := context.Background()
	if err := goose.UpToContext(ctx, d, "migrations", 9); err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, `INSERT INTO media_assets (filename, content_type, kind, size_bytes) VALUES ('a.jpg', 'image/jpeg', 'image', 1)`)
	mustExec(t, d, `INSERT INTO scheduled_posts (kind, media_ids, status) VALUES ('image', '{1}', 'published')`)
	mustExec(t, d, `INSERT INTO auto_rules (name, keywords, reply, hit_count) VALUES ('菜單', '{菜單}', 'x', 4)`)
	if err := goose.UpToContext(ctx, d, "migrations", 10); err != nil {
		t.Fatal(err)
	}
	var id int64
	var active bool
	if err := d.QueryRow(`SELECT id, active FROM ig_accounts WHERE ig_user_id = 'migrated-placeholder'`).Scan(&id, &active); err != nil || active {
		t.Fatalf("placeholder: id=%d active=%v err=%v", id, active, err)
	}
	if intOf(t, d, `SELECT ig_account_id FROM scheduled_posts`) != id || intOf(t, d, `SELECT ig_account_id FROM media_assets`) != id {
		t.Fatal("貼文／媒體應掛佔位帳號")
	}
	if intOf(t, d, `SELECT count(*) FROM auto_rules WHERE ig_account_id IS NULL AND id = 1 AND hit_count = 4`) != 1 ||
		intOf(t, d, `SELECT count(*) FROM automation_settings WHERE ig_account_id IS NULL`) != 1 {
		t.Fatal("沒有帳號時既有自動回覆資料應成為範本")
	}
	if err := goose.DownToContext(ctx, d, "migrations", 9); err != nil {
		t.Fatal(err)
	}
	if intOf(t, d, `SELECT count(*) FROM ig_accounts`) != 0 || intOf(t, d, `SELECT count(*) FROM auto_rules`) != 1 {
		t.Fatal("down: 佔位帳號應移除、範本規則保留")
	}
}

// 00012：既有顧客、對話、訊息、自動回覆設定原封不動；新欄位都帶預設（顧客未標記；每列設定含範本都是 10 則／300 秒／判斷騷擾開）。
// Down 再 Up 也要能跑，且資料保留。
func TestMigration12FlagsPreservesData(t *testing.T) {
	d := isolated(t)
	ctx := context.Background()
	if err := goose.UpToContext(ctx, d, "migrations", 11); err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, `INSERT INTO ig_accounts (ig_user_id, username) VALUES ('acc-a', 'a'), ('acc-b', 'b')`)
	mustExec(t, d, `INSERT INTO contacts (ig_account_id, igsid, username, note) VALUES (1, 'u1', 'alice', '常客'), (2, 'u2', 'bob', '')`)
	mustExec(t, d, `INSERT INTO conversations (contact_id, handler) VALUES (1, 'human'), (2, 'bot')`)
	mustExec(t, d, `INSERT INTO messages (conversation_id, ig_mid, direction, sender_type, text) VALUES
(1, 'm1', 'in', 'contact', '你好'), (1, 'm2', 'out', 'ai', '哈囉')`)
	mustExec(t, d, `INSERT INTO automation_settings (ig_account_id, persona, debounce_secs) VALUES (1, 'A 人設', 7)`)
	mustExec(t, d, `UPDATE automation_settings SET persona = '範本人設' WHERE ig_account_id IS NULL`)
	settingsBefore := intOf(t, d, `SELECT count(*) FROM automation_settings`)

	if err := goose.UpToContext(ctx, d, "migrations", 12); err != nil {
		t.Fatal(err)
	}
	if intOf(t, d, `SELECT count(*) FROM contacts`) != 2 || intOf(t, d, `SELECT count(*) FROM conversations`) != 2 ||
		intOf(t, d, `SELECT count(*) FROM messages`) != 2 || intOf(t, d, `SELECT count(*) FROM automation_settings`) != settingsBefore {
		t.Fatal("核心資料數量變了")
	}
	if strOf(t, d, `SELECT note FROM contacts WHERE id = 1`) != "常客" || strOf(t, d, `SELECT handler FROM conversations WHERE id = 1`) != "human" {
		t.Fatal("既有欄位值變了")
	}
	if intOf(t, d, `SELECT count(*) FROM contacts WHERE risk = '' AND risk_reason = '' AND risk_source = '' AND risk_at IS NULL AND risk_by_admin_id IS NULL`) != 2 {
		t.Fatal("既有顧客應為未標記")
	}
	if intOf(t, d, `SELECT count(*) FROM automation_settings WHERE spam_max_msgs = 10 AND spam_window_secs = 300 AND ai_moderation`) != settingsBefore {
		t.Fatal("既有設定（含範本）應帶預設門檻與騷擾判斷")
	}
	if strOf(t, d, `SELECT persona FROM automation_settings WHERE ig_account_id = 1`) != "A 人設" ||
		strOf(t, d, `SELECT persona FROM automation_settings WHERE ig_account_id IS NULL`) != "範本人設" {
		t.Fatal("既有設定值變了")
	}
	// 約束：標記值、未處理的同訊息同種只能一筆
	if _, err := d.Exec(`UPDATE contacts SET risk = 'evil' WHERE id = 1`); err == nil {
		t.Fatal("risk 應有 CHECK")
	}
	mustExec(t, d, `INSERT INTO message_reviews (message_id, ig_account_id, kind, note) VALUES (2, 1, 'ai_wrong', '錯')`)
	if _, err := d.Exec(`INSERT INTO message_reviews (message_id, ig_account_id, kind) VALUES (2, 1, 'ai_wrong')`); err == nil {
		t.Fatal("未處理的同訊息同種應只能一筆")
	}
	mustExec(t, d, `UPDATE message_reviews SET resolved_at = now()`)
	mustExec(t, d, `INSERT INTO message_reviews (message_id, ig_account_id, kind) VALUES (2, 1, 'ai_wrong')`)
	mustExec(t, d, `UPDATE contacts SET risk = 'blocked', risk_source = 'rate', risk_at = now() WHERE id = 2`)

	if err := goose.DownToContext(ctx, d, "migrations", 11); err != nil {
		t.Fatal(err)
	}
	if intOf(t, d, `SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema()
  AND ((table_name = 'contacts' AND column_name LIKE 'risk%') OR (table_name = 'automation_settings' AND column_name IN ('spam_max_msgs', 'spam_window_secs', 'ai_moderation')))`) != 0 {
		t.Fatal("down: 新欄位應移除")
	}
	if intOf(t, d, `SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'message_reviews'`) != 0 {
		t.Fatal("down: message_reviews 應移除")
	}
	if intOf(t, d, `SELECT count(*) FROM contacts`) != 2 || intOf(t, d, `SELECT count(*) FROM messages`) != 2 ||
		intOf(t, d, `SELECT count(*) FROM automation_settings`) != settingsBefore {
		t.Fatal("down: 既有資料應保留")
	}
	if err := goose.UpContext(ctx, d, "migrations"); err != nil {
		t.Fatal(err)
	}
}

// 00013：降頻等級欄位，既有顧客一律 0（正常）；Down 再 Up 可重跑。
func TestMigration13ThrottlePreservesData(t *testing.T) {
	d := isolated(t)
	ctx := context.Background()
	if err := goose.UpToContext(ctx, d, "migrations", 12); err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, `INSERT INTO ig_accounts (ig_user_id) VALUES ('acc-a')`)
	mustExec(t, d, `INSERT INTO contacts (ig_account_id, igsid, risk, risk_source) VALUES (1, 'u1', 'watch', 'manual'), (1, 'u2', '', '')`)
	if err := goose.UpToContext(ctx, d, "migrations", 13); err != nil {
		t.Fatal(err)
	}
	if intOf(t, d, `SELECT count(*) FROM contacts WHERE throttle_level = 0`) != 2 ||
		strOf(t, d, `SELECT risk FROM contacts WHERE igsid = 'u1'`) != "watch" {
		t.Fatal("既有顧客應為 0 級、標記保留")
	}
	if _, err := d.Exec(`UPDATE contacts SET throttle_level = -1`); err == nil {
		t.Fatal("throttle_level 應有 CHECK")
	}
	if err := goose.DownToContext(ctx, d, "migrations", 12); err != nil {
		t.Fatal(err)
	}
	if intOf(t, d, `SELECT count(*) FROM contacts`) != 2 {
		t.Fatal("down: 顧客應保留")
	}
	if err := goose.UpContext(ctx, d, "migrations"); err != nil {
		t.Fatal(err)
	}
}

// 00014：單一登入欄位與操作紀錄表；既有管理員資料保留、沒有 session（升級後都要重新登入）。Down 再 Up 也要能跑。
func TestMigration14SessionAuditPreservesData(t *testing.T) {
	d := isolated(t)
	ctx := context.Background()
	if err := goose.UpToContext(ctx, d, "migrations", 13); err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, `INSERT INTO admin_users (username, password_hash, display_name, is_owner, permissions, last_login_at)
VALUES ('boss', 'h1', '老闆', true, '{}', now()), ('mei', 'h2', '小美', false, '{inbox,contacts}', NULL)`)
	if err := goose.UpToContext(ctx, d, "migrations", 14); err != nil {
		t.Fatal(err)
	}
	if intOf(t, d, `SELECT count(*) FROM admin_users WHERE session_id IS NULL AND last_seen_at IS NULL`) != 2 ||
		strOf(t, d, `SELECT display_name || password_hash || array_to_string(permissions, ',') FROM admin_users WHERE username = 'mei'`) != "小美h2inbox,contacts" ||
		intOf(t, d, `SELECT count(*) FROM admin_users WHERE username = 'boss' AND is_owner AND last_login_at IS NOT NULL`) != 1 {
		t.Fatal("既有管理員資料應保留、沒有 session")
	}
	mustExec(t, d, `INSERT INTO audit_logs (admin_id, admin_name, action, detail) VALUES (1, '老闆', 'auth.login', '{}'),
  (NULL, 'ghost', 'auth.login_failed', '{"username":"ghost"}')`)
	mustExec(t, d, `INSERT INTO audit_logs (admin_id, action, detail) VALUES (1, 'auth.kicked', '{"session":"abc"}')`)
	if _, err := d.Exec(`INSERT INTO audit_logs (admin_id, action, detail) VALUES (1, 'auth.kicked', '{"session":"abc"}')`); err == nil {
		t.Fatal("同一個舊 session 的被踢紀錄應只能一筆")
	}
	// 管理員被刪：紀錄保留、admin_id 置空
	mustExec(t, d, `DELETE FROM admin_users WHERE username = 'boss'`)
	if intOf(t, d, `SELECT count(*) FROM audit_logs WHERE admin_id IS NULL`) != 3 {
		t.Fatal("刪管理員後紀錄應保留")
	}
	if err := goose.DownToContext(ctx, d, "migrations", 13); err != nil {
		t.Fatal(err)
	}
	if strOf(t, d, `SELECT display_name FROM admin_users WHERE username = 'mei'`) != "小美" {
		t.Fatal("down: 管理員應保留")
	}
	if err := goose.UpContext(ctx, d, "migrations"); err != nil {
		t.Fatal(err)
	}
}

// 00015：既有自動回覆設定（範本＋帳號）保留、總開關預設開、摘要時間預設 09:00；daily_digests 每帳號每天一份、
// 帳號刪除時一併刪；摘要時間有格式檢查。Down 再 Up 也要能跑。
func TestMigration15DashboardPreservesData(t *testing.T) {
	d := isolated(t)
	ctx := context.Background()
	if err := goose.UpToContext(ctx, d, "migrations", 14); err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, `INSERT INTO ig_accounts (ig_user_id) VALUES ('acc-a'), ('acc-b')`)
	mustExec(t, d, `DELETE FROM automation_settings`)
	mustExec(t, d, `INSERT INTO automation_settings (ig_account_id, persona, ai_enabled, spam_max_msgs) VALUES (NULL, '範本', true, 5), (1, 'A 店', false, 7)`)
	if err := goose.UpToContext(ctx, d, "migrations", 15); err != nil {
		t.Fatal(err)
	}
	if intOf(t, d, `SELECT count(*) FROM automation_settings WHERE auto_reply_enabled AND digest_time = '09:00'`) != 2 ||
		strOf(t, d, `SELECT persona || ai_enabled::text || spam_max_msgs::text FROM automation_settings WHERE ig_account_id = 1`) != "A 店false7" {
		t.Fatal("既有設定應保留、總開關開、摘要時間 09:00")
	}
	if _, err := d.Exec(`UPDATE automation_settings SET digest_time = '9:00'`); err == nil {
		t.Fatal("digest_time 應有格式檢查")
	}
	mustExec(t, d, `INSERT INTO daily_digests (ig_account_id, digest_date, content) VALUES (1, '2026-11-11', '{"todo":[]}'), (2, '2026-11-11', '{}')`)
	if _, err := d.Exec(`INSERT INTO daily_digests (ig_account_id, digest_date) VALUES (1, '2026-11-11')`); err == nil {
		t.Fatal("同一帳號同一天只能一份")
	}
	mustExec(t, d, `DELETE FROM ig_accounts WHERE ig_user_id = 'acc-b'`)
	if intOf(t, d, `SELECT count(*) FROM daily_digests`) != 1 {
		t.Fatal("帳號刪除時摘要一併刪")
	}
	if err := goose.DownToContext(ctx, d, "migrations", 14); err != nil {
		t.Fatal(err)
	}
	if strOf(t, d, `SELECT persona FROM automation_settings WHERE ig_account_id = 1`) != "A 店" ||
		intOf(t, d, `SELECT count(*) FROM automation_settings`) != 2 {
		t.Fatal("down: 設定應保留")
	}
	if err := goose.UpContext(ctx, d, "migrations"); err != nil {
		t.Fatal(err)
	}
}

// 00016：建立預設超管 superAdmin（admin123、必須改密碼）；既有管理員資料保留、ig_accounts／credentials 勾選拿掉；
// 再跑一次（Down 再 Up）不重複建立。
func TestMigration16SeedsSuperAdmin(t *testing.T) {
	d := isolated(t)
	ctx := context.Background()
	if err := goose.UpToContext(ctx, d, "migrations", 15); err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, `INSERT INTO admin_users (username, password_hash, display_name, is_owner, permissions, session_id, last_login_at)
VALUES ('admin', 'h1', '老闆', true, '{}', 'sid-1', now()),
       ('mei', 'h2', '小美', false, '{inbox,ig_accounts,credentials,audit}', NULL, NULL)`)
	if err := goose.UpToContext(ctx, d, "migrations", 16); err != nil {
		t.Fatal(err)
	}
	var hash string
	var super, mustChange, owner bool
	var perms, disabled int64
	if err := d.QueryRow(`SELECT password_hash, is_super, must_change_password, is_owner, cardinality(permissions),
  (disabled_at IS NOT NULL)::int FROM admin_users WHERE username = 'superAdmin'`).Scan(&hash, &super, &mustChange, &owner, &perms, &disabled); err != nil {
		t.Fatal(err)
	}
	if !super || !mustChange || owner || perms != 0 || disabled != 0 ||
		bcrypt.CompareHashAndPassword([]byte(hash), []byte("admin123")) != nil {
		t.Fatalf("seeded superAdmin: super=%v mustChange=%v owner=%v", super, mustChange, owner)
	}
	if strOf(t, d, `SELECT display_name || password_hash || array_to_string(permissions, ',') || session_id FROM admin_users WHERE username = 'admin'`) != "老闆h1sid-1" ||
		intOf(t, d, `SELECT count(*) FROM admin_users WHERE username = 'admin' AND is_owner AND NOT is_super AND NOT must_change_password
  AND totp_secret IS NULL AND totp_enabled_at IS NULL`) != 1 {
		t.Fatal("既有店長應保留、不變成超管")
	}
	if strOf(t, d, `SELECT array_to_string(permissions, ',') FROM admin_users WHERE username = 'mei'`) != "inbox,audit" {
		t.Fatal("ig_accounts／credentials 勾選應拿掉、其他保留")
	}
	// 冪等：Down 再 Up 只會有一位 superAdmin
	if err := goose.DownToContext(ctx, d, "migrations", 15); err != nil {
		t.Fatal(err)
	}
	if intOf(t, d, `SELECT count(*) FROM admin_users WHERE username = 'superAdmin'`) != 0 ||
		intOf(t, d, `SELECT count(*) FROM admin_users`) != 2 {
		t.Fatal("down: 沒登入過的預設超管應刪除、其他保留")
	}
	if err := goose.UpToContext(ctx, d, "migrations", 16); err != nil {
		t.Fatal(err)
	}
	if intOf(t, d, `SELECT count(*) FROM admin_users WHERE username = 'superAdmin' AND is_super`) != 1 {
		t.Fatal("再 Up 應重新建立一位 superAdmin")
	}
	if err := goose.UpContext(ctx, d, "migrations"); err != nil {
		t.Fatal(err)
	}
}

// 00016：已有同名 superAdmin 帳號時不覆蓋（ON CONFLICT DO NOTHING）；用過的超管 Down 時停用而非刪除。
func TestMigration16KeepsExistingSuperAdminName(t *testing.T) {
	d := isolated(t)
	ctx := context.Background()
	if err := goose.UpToContext(ctx, d, "migrations", 15); err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, `INSERT INTO admin_users (username, password_hash, display_name, is_owner) VALUES ('superAdmin', 'mine', '我', true)`)
	if err := goose.UpToContext(ctx, d, "migrations", 16); err != nil {
		t.Fatal(err)
	}
	if intOf(t, d, `SELECT count(*) FROM admin_users WHERE username = 'superAdmin' AND password_hash = 'mine' AND is_owner
  AND NOT is_super AND NOT must_change_password`) != 1 || intOf(t, d, `SELECT count(*) FROM admin_users`) != 1 {
		t.Fatal("既有同名帳號應原封不動")
	}
	mustExec(t, d, `INSERT INTO admin_users (username, password_hash, is_super, last_login_at) VALUES ('root', 'h', true, now())`)
	if err := goose.DownToContext(ctx, d, "migrations", 15); err != nil {
		t.Fatal(err)
	}
	if intOf(t, d, `SELECT count(*) FROM admin_users WHERE username = 'root' AND disabled_at IS NOT NULL`) != 1 ||
		intOf(t, d, `SELECT count(*) FROM admin_users WHERE username = 'superAdmin' AND disabled_at IS NULL`) != 1 {
		t.Fatal("down: 超管應停用、非超管不動")
	}
}

// 00017：蒸餾表；既有自動回覆設定保留且每週蒸餾預設關；同帳號只能一筆 running；帳號刪除時一併刪；Down 再 Up 可重跑。
func TestMigration17DistillPreservesData(t *testing.T) {
	d := isolated(t)
	ctx := context.Background()
	if err := goose.UpToContext(ctx, d, "migrations", 16); err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, `INSERT INTO ig_accounts (ig_user_id) VALUES ('acc-a'), ('acc-b')`)
	mustExec(t, d, `DELETE FROM automation_settings`)
	mustExec(t, d, `INSERT INTO automation_settings (ig_account_id, persona, digest_time, auto_reply_enabled) VALUES (NULL, '範本', '08:30', true), (1, 'A 店', '10:00', false)`)
	if err := goose.UpToContext(ctx, d, "migrations", 17); err != nil {
		t.Fatal(err)
	}
	if intOf(t, d, `SELECT count(*) FROM automation_settings WHERE NOT distill_weekly`) != 2 ||
		strOf(t, d, `SELECT persona || digest_time || auto_reply_enabled::text FROM automation_settings WHERE ig_account_id = 1`) != "A 店10:00false" {
		t.Fatal("既有設定應保留、每週蒸餾預設關")
	}
	mustExec(t, d, `INSERT INTO distill_runs (ig_account_id, days) VALUES (1, 30), (2, 7)`)
	if _, err := d.Exec(`INSERT INTO distill_runs (ig_account_id, days) VALUES (1, 30)`); err == nil {
		t.Fatal("同帳號只能一筆 running")
	}
	if _, err := d.Exec(`INSERT INTO distill_runs (ig_account_id, days, status) VALUES (1, 14, 'done')`); err == nil {
		t.Fatal("days 只收 7／30／90")
	}
	mustExec(t, d, `INSERT INTO distill_suggestions (run_id, ig_account_id, payload) VALUES (1, 1, '{"type":"rule"}'), (2, 2, '{"type":"knowledge"}')`)
	if _, err := d.Exec(`UPDATE distill_suggestions SET status = 'superseded'`); err == nil {
		t.Fatal("status 只收 pending／accepted／dismissed")
	}
	mustExec(t, d, `DELETE FROM ig_accounts WHERE ig_user_id = 'acc-b'`)
	if intOf(t, d, `SELECT count(*) FROM distill_runs`) != 1 || intOf(t, d, `SELECT count(*) FROM distill_suggestions`) != 1 {
		t.Fatal("帳號刪除時蒸餾紀錄一併刪")
	}
	if err := goose.DownToContext(ctx, d, "migrations", 16); err != nil {
		t.Fatal(err)
	}
	if strOf(t, d, `SELECT persona || digest_time FROM automation_settings WHERE ig_account_id = 1`) != "A 店10:00" ||
		intOf(t, d, `SELECT count(*) FROM automation_settings`) != 2 {
		t.Fatal("down: 設定應保留")
	}
	if err := goose.UpContext(ctx, d, "migrations"); err != nil {
		t.Fatal(err)
	}
}
