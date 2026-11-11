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
