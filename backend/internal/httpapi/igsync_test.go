package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mike504110403/litty-coffee/backend/internal/auth"
	"github.com/mike504110403/litty-coffee/backend/internal/automation"
	"github.com/mike504110403/litty-coffee/backend/internal/posts"
)

func enableMediaSync(e *fullEnv) {
	e.s.MediaSync = &posts.MediaSyncer{Svc: e.s.Posts, IG: e.pub, Creds: e.s.Accounts.Credentials, Insights: e.s.Posts.Insights}
}

func syncPath(acc int64) string { return fmt.Sprintf("/api/ig/accounts/%d/sync-media", acc) }

// 手動同步：排程權限＋帳號在可見範圍內（範圍外、不存在 404；沒排程權限 403）；每帳號 1 分鐘冷卻 429＋Retry-After。
// 同步進來的貼文出現在清單（source／igThumb／aiExclude），唯讀：改、刪 409；可切 aiExclude、更新成效。
func TestIGSyncMediaAPI(t *testing.T) {
	e, owner, A, B := setupAudit(t)
	enableMediaSync(e)
	noSched := staff(t, e, owner, "inboxer", []string{auth.PermInbox})
	schedA := staff(t, e, owner, "scheda", []string{auth.PermSchedule}, A)

	if got := noSched.status("POST", syncPath(A), ""); got != http.StatusForbidden {
		t.Fatalf("沒有排程權限：%d", got)
	}
	schedA.json("POST", syncPath(B), "", http.StatusNotFound)
	owner.json("POST", syncPath(99999), "", http.StatusNotFound)
	res := decode[posts.SyncResult](t, schedA.json("POST", syncPath(A), "", http.StatusOK))
	if res.Posts != 3 || res.Stories != 1 {
		t.Fatalf("sync result: %+v", res)
	}
	r := owner.json("POST", syncPath(A), "", http.StatusTooManyRequests)
	if ra, err := strconv.Atoi(r.Header.Get("Retry-After")); err != nil || ra < 1 || ra > 60 {
		t.Fatalf("Retry-After: %q", r.Header.Get("Retry-After"))
	}
	owner.json("POST", syncPath(B), "", http.StatusOK) // 冷卻是每帳號各自計
	if row := auditRows(t, e, "ig.sync_media"); len(row) < 2 || row[0].Account == nil || *row[0].Account != A || row[0].Summary != "同步 IG 貼文" {
		t.Fatalf("audit: %+v", row)
	}

	list := decode[[]posts.Post](t, schedA.json("GET", fmt.Sprintf("/api/posts?account=%d", A), "", 200))
	if len(list) != 4 {
		t.Fatalf("list: %d", len(list))
	}
	kinds := map[string]bool{}
	var post, story posts.Post
	for _, p := range list {
		kinds[p.Kind] = true
		if p.Source != "ig" || p.AIExclude || !strings.HasPrefix(p.IGThumb, "/media/ig-") || p.Status != "published" || p.IGAccountID != A {
			t.Fatalf("synced post: %+v", p)
		}
		if p.Kind == "story" {
			story = p
		} else if p.Kind == "image" {
			post = p
		}
	}
	if !kinds["image"] || !kinds["carousel"] || !kinds["reel"] || !kinds["story"] || story.InsightsAt == nil {
		t.Fatalf("kinds=%v story=%+v", kinds, story)
	}
	img, err := http.Get(e.srv.URL + post.IGThumb)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(img.Body)
	img.Body.Close()
	if img.StatusCode != 200 || http.DetectContentType(b) != "image/jpeg" {
		t.Fatalf("縮圖應可經 /media 取得：%d %s", img.StatusCode, http.DetectContentType(b))
	}
	if n := len(decode[[]posts.Post](t, owner.json("GET", fmt.Sprintf("/api/posts?account=%d&source=app", A), "", 200))); n != 0 {
		t.Fatalf("source=app: %d", n)
	}
	owner.json("GET", "/api/posts?source=x", "", http.StatusBadRequest)

	// 唯讀
	body := fmt.Sprintf(`{"action":"draft","post":{"igAccountId":%d,"kind":"image","caption":"偷改","mediaIds":[]}}`, A)
	if r := schedA.json("PUT", fmt.Sprintf("/api/posts/%d", post.ID), body, http.StatusConflict); !strings.Contains(readAll(r), "IG 同步") {
		t.Fatal("改內容應 409 並說明")
	}
	schedA.json("PUT", fmt.Sprintf("/api/posts/%d", post.ID), strings.Replace(body, "draft", "now", 1), http.StatusConflict)
	schedA.json("DELETE", fmt.Sprintf("/api/posts/%d", post.ID), "", http.StatusConflict)
	got := decode[posts.Post](t, schedA.json("PATCH", fmt.Sprintf("/api/posts/%d/ai-exclude", post.ID), `{"aiExclude":true}`, 200))
	if !got.AIExclude || got.Source != "ig" {
		t.Fatalf("ai-exclude: %+v", got)
	}
	schedA.json("PATCH", fmt.Sprintf("/api/posts/%d/ai-exclude", post.ID), `{}`, http.StatusBadRequest)
	schedA.json("POST", fmt.Sprintf("/api/posts/%d/insights/refresh", post.ID), "", 200)
	var postB int64
	if err := e.pool.QueryRow(context.Background(), `SELECT id FROM scheduled_posts WHERE ig_account_id = $1 LIMIT 1`, B).Scan(&postB); err != nil {
		t.Fatal(err)
	}
	schedA.json("PATCH", fmt.Sprintf("/api/posts/%d/ai-exclude", postB), `{"aiExclude":true}`, http.StatusNotFound)
	if rows := auditRows(t, e, "post.ai_exclude"); len(rows) != 3 || rows[0].Account == nil || *rows[0].Account != A || !rows[0].Success ||
		rows[2].Status != http.StatusNotFound { // 成功、400、範圍外 404 都記
		t.Fatalf("audit ai_exclude: %+v", rows)
	}
}

func seedHTTPPost(t *testing.T, e *fullEnv, acc int64, caption string, exclude bool, at time.Time, mediaID string) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(), `
INSERT INTO scheduled_posts (ig_account_id, kind, status, caption, source, ai_exclude, published_at, scheduled_at, ig_media_id)
VALUES ($1, 'carousel', 'published', $2, 'ig', $3, $4, $4, $5)`, acc, caption, exclude, at, mediaID); err != nil {
		t.Fatal(err)
	}
}

// AI 建議回覆與蒸餾：帶「對話／蒸餾帳號」的近期貼文（排除 ai_exclude、不含別的帳號），日期在 prompt 裡；設定關閉就不帶。
func TestPostsKBForSuggestAndDistill(t *testing.T) {
	e := setupSuggest(t)
	at := time.Now().Add(-48 * time.Hour)
	seedHTTPPost(t, e.fullEnv, e.A, "A店會員日 9 折", false, at, "ma1")
	seedHTTPPost(t, e.fullEnv, e.A, "A店排除的貼文", true, at, "ma2")
	seedHTTPPost(t, e.fullEnv, e.B, "B店貼文", false, at, "mb1")

	settings := decode[automation.Settings](t, e.owner.json("GET", fmt.Sprintf("/api/automation/settings?account=%d", e.A), "", 200))
	if !settings.PostsKBEnabled {
		t.Fatal("postsKbEnabled 預設應為 true")
	}
	e.owner.json("POST", suggestPath(e.convA), "", http.StatusOK)
	prompt := automation.SuggestSystemPrompt(e.ai.st, e.ai.kb)
	date := at.In(time.Local).Format("2006-01-02")
	if !strings.Contains(prompt, "["+date+"｜輪播貼文]\nA店會員日 9 折") || strings.Contains(prompt, "排除的") || strings.Contains(prompt, "B店貼文") {
		t.Fatalf("prompt：\n%s", prompt)
	}

	e.owner.json("PUT", fmt.Sprintf("/api/automation/settings?account=%d", e.A), `{"postsKbEnabled":false}`, 200)
	if s := decode[automation.Settings](t, e.owner.json("GET", fmt.Sprintf("/api/automation/settings?account=%d", e.A), "", 200)); s.PostsKBEnabled || s.Persona == "" {
		t.Fatalf("設定應可關閉且其他欄位保留：%+v", s)
	}
	e.owner.json("POST", suggestPath(e.convA), "", http.StatusOK)
	if e.ai.st.RecentPosts != nil || strings.Contains(automation.SuggestSystemPrompt(e.ai.st, e.ai.kb), "近期 IG 貼文") {
		t.Fatal("關閉時不帶貼文")
	}
}

func TestPostsKBForDistill(t *testing.T) {
	e := setupDistill(t)
	e.standardOutput()
	at := time.Now().Add(-72 * time.Hour)
	seedHTTPPost(t, e.fullEnv, e.A, "A店新品上市", false, at, "ma1")
	seedHTTPPost(t, e.fullEnv, e.B, "B店貼文", false, at, "mb1")
	e.owner.json("POST", distillPath(e.A, 30), "", http.StatusOK)
	if len(e.ai.in.Posts) != 1 || e.ai.in.Posts[0].Caption != "A店新品上市" || e.ai.in.Posts[0].Date != at.In(time.Local).Format("2006-01-02") {
		t.Fatalf("distill posts: %+v", e.ai.in.Posts)
	}
	if sys := automation.DistillSystemPrompt(e.ai.in.Rules, e.ai.in.Knowledge, e.ai.in.Posts); !strings.Contains(sys, "A店新品上市") ||
		strings.Contains(sys, "B店貼文") {
		t.Fatal("distill prompt 應只含 A 的貼文")
	}
}
