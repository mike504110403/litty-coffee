package inbox

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func event(t *testing.T, sender, recipient string, ts time.Time, body map[string]any) []byte {
	m := map[string]any{"sender": map[string]string{"id": sender}, "recipient": map[string]string{"id": recipient}, "timestamp": ts.UnixMilli()}
	for k, v := range body {
		m[k] = v
	}
	b, err := json.Marshal(map[string]any{"object": "instagram", "entry": []any{map[string]any{"id": "acct", "time": ts.UnixMilli(), "messaging": []any{m}}}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func enqueue(t *testing.T, s *Service, payloads ...[]byte) {
	for _, p := range payloads {
		if err := s.EnqueueWebhook(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	drain(t, s)
}

func TestReadReactionUnsend(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Millisecond)
	enqueue(t, s,
		payload(t, "u1", "acct", "m1", "想訂位", false, now),
		payload(t, "u1", "acct", "m2", "打錯了", false, now.Add(time.Second)),
		payload(t, "acct", "u1", "m3", "好的，幾位呢？", true, now.Add(2*time.Second)),
	)
	convs, _ := s.ListConversations(ctx, ListFilter{})
	c := convs[0]

	enqueue(t, s,
		event(t, "u1", "acct", now.Add(3*time.Second), map[string]any{"read": map[string]string{"mid": "m3"}}),
		event(t, "u1", "acct", now.Add(4*time.Second), map[string]any{"reaction": map[string]string{"mid": "m3", "action": "react", "reaction": "love", "emoji": "❤️"}}),
		event(t, "acct", "u1", now.Add(4*time.Second), map[string]any{"reaction": map[string]string{"mid": "m1", "action": "react", "emoji": "👍"}}), // 我方按的：忽略
		event(t, "u1", "acct", now.Add(5*time.Second), map[string]any{"message": map[string]any{"mid": "m2", "is_deleted": true}}),
		event(t, "u9", "acct", now, map[string]any{"read": map[string]string{"mid": "x"}}), // 不認識的人：忽略
	)
	c, _ = s.GetConversation(ctx, c.ID)
	if c.ContactReadAt == nil || !c.ContactReadAt.Equal(now.Add(3*time.Second)) {
		t.Fatalf("read at %v", c.ContactReadAt)
	}
	msgs, _ := s.ListMessages(ctx, c.ID, 0, 50)
	if msgs[0].Reaction != "" || msgs[2].Reaction != "❤️" {
		t.Fatalf("reactions: %q %q", msgs[0].Reaction, msgs[2].Reaction)
	}
	if !msgs[1].Deleted || msgs[1].Text != "" {
		t.Fatalf("unsend: %+v", msgs[1])
	}
	// 收回的不是最後一則 → 列表摘要不變
	if c.LastMessageText != "好的，幾位呢？" {
		t.Fatalf("last text %q", c.LastMessageText)
	}

	// 取消表情；舊的已讀時間不會倒退
	enqueue(t, s,
		event(t, "u1", "acct", now.Add(6*time.Second), map[string]any{"reaction": map[string]string{"mid": "m3", "action": "unreact"}}),
		event(t, "u1", "acct", now, map[string]any{"read": map[string]string{"mid": "m1"}}),
	)
	msgs, _ = s.ListMessages(ctx, c.ID, 0, 50)
	c, _ = s.GetConversation(ctx, c.ID)
	if msgs[2].Reaction != "" || !c.ContactReadAt.Equal(now.Add(3*time.Second)) {
		t.Fatalf("unreact/read regress: %q %v", msgs[2].Reaction, c.ContactReadAt)
	}
	var errs int
	_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM webhook_events WHERE error <> ''`).Scan(&errs)
	if errs != 0 {
		t.Fatalf("%d events errored", errs)
	}
}

func pngData(t *testing.T) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestAttachmentDownload(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	s.MediaDir = t.TempDir()
	img := pngData(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.png":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(img)
		case "/html":
			_, _ = w.Write([]byte("<html><script>alert(1)</script></html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	att := func(mid, url string, ts time.Time) []byte {
		return event(t, "u1", "acct", ts, map[string]any{"message": map[string]any{"mid": mid,
			"attachments": []any{map[string]any{"type": "image", "payload": map[string]string{"url": url}}}}})
	}
	now := time.Now()
	enqueue(t, s, att("a1", srv.URL+"/ok.png", now), att("a2", srv.URL+"/gone", now.Add(time.Second)),
		att("a3", srv.URL+"/html", now.Add(2*time.Second)))
	convs, _ := s.ListConversations(ctx, ListFilter{})
	msgs, _ := s.ListMessages(ctx, convs[0].ID, 0, 50)
	if !strings.HasPrefix(msgs[0].MediaURL, MediaRoute) || !strings.HasSuffix(msgs[0].MediaURL, ".png") {
		t.Fatalf("saved url %q", msgs[0].MediaURL)
	}
	name := strings.TrimPrefix(msgs[0].MediaURL, MediaRoute)
	if got, _ := os.ReadFile(filepath.Join(s.MediaDir, "inbox", name)); !bytes.Equal(got, img) {
		t.Fatal("file content mismatch")
	}
	// 下載失敗、非媒體內容：保留原網址並記錯誤
	for i, want := range []string{"HTTP 404", "不支援的附件格式"} {
		m := msgs[i+1]
		var errText string
		_ = s.DB.QueryRow(ctx, `SELECT media_error FROM messages WHERE id = $1`, m.ID).Scan(&errText)
		if !strings.HasPrefix(m.MediaURL, srv.URL) || !strings.Contains(errText, want) {
			t.Fatalf("msg %d: url=%q err=%q", i+1, m.MediaURL, errText)
		}
	}

	// 經 handler 取檔；路徑穿越擋掉
	rec := httptest.NewRecorder()
	s.MediaHandler(rec, httptest.NewRequest("GET", "/x", nil), name)
	if rec.Code != 200 || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("serve %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.MediaHandler(rec, httptest.NewRequest("GET", "/x", nil), "../inbox")
	if rec.Code != 404 {
		t.Fatalf("traversal %d", rec.Code)
	}

	// 顧客收回 → 本地檔一併刪除
	enqueue(t, s, event(t, "u1", "acct", now.Add(3*time.Second), map[string]any{"message": map[string]any{"mid": "a1", "is_deleted": true}}))
	if _, err := os.Stat(filepath.Join(s.MediaDir, "inbox", name)); !os.IsNotExist(err) {
		t.Fatalf("file should be removed: %v", err)
	}
	m, _ := s.GetMessage(ctx, msgs[0].ID)
	if !m.Deleted || m.MediaURL != "" {
		t.Fatalf("after unsend %+v", m)
	}
}
