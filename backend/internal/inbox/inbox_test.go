package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/mike504110403/litty-coffee/backend/internal/db"
	"github.com/mike504110403/litty-coffee/backend/internal/ig"
	"github.com/mike504110403/litty-coffee/backend/internal/realtime"
)

func TestWindowCheck(t *testing.T) {
	now := time.Now()
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	cases := []struct {
		name      string
		last      *time.Time
		sender    string
		wantTag   bool
		wantError bool
	}{
		{"never messaged", nil, "human", false, true},
		{"within 24h bot", ago(23 * time.Hour), "bot", false, false},
		{"within 24h human", ago(time.Hour), "human", false, false},
		{"after 24h human uses tag", ago(30 * time.Hour), "human", true, false},
		{"after 24h ai blocked", ago(30 * time.Hour), "ai", false, true},
		{"after 7d human blocked", ago(8 * 24 * time.Hour), "human", false, true},
	}
	for _, c := range cases {
		tag, err := windowCheck(c.last, c.sender, now)
		if tag != c.wantTag || (err != nil) != c.wantError {
			t.Errorf("%s: tag=%v err=%v", c.name, tag, err)
		}
	}
}

// 整合測試：需要地端 PG（docker compose），TEST_DATABASE_URL 未設時跳過。
func newTestService(t *testing.T) (*Service, *ig.Fake) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if err := db.Migrate(url); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `TRUNCATE webhook_events, messages, conversations, contacts, ig_accounts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	fake := ig.NewFake()
	return NewService(pool, fake, realtime.NewHub()), fake
}

func payload(t *testing.T, sender, recipient, mid, text string, echo bool, ts time.Time) []byte {
	msg := map[string]any{"mid": mid, "text": text}
	if echo {
		msg["is_echo"] = true
	}
	b, err := json.Marshal(map[string]any{"object": "instagram", "entry": []any{map[string]any{
		"id": "acct", "time": ts.UnixMilli(),
		"messaging": []any{map[string]any{"sender": map[string]string{"id": sender}, "recipient": map[string]string{"id": recipient},
			"timestamp": ts.UnixMilli(), "message": msg}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func drain(t *testing.T, s *Service) {
	for {
		done, err := s.processOne(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if done {
			return
		}
	}
}

func TestIngestAndSend(t *testing.T) {
	s, fake := newTestService(t)
	ctx := context.Background()
	fake.Register("u1", ig.Profile{Username: "latte_lover", Name: "小拿"})

	var inbound []Message
	s.OnInbound = func(_ int64, m Message) { inbound = append(inbound, m) }

	now := time.Now()
	in := payload(t, "u1", "acct", "m1", "請問今天有營業嗎", false, now)
	for range 2 { // 重送同一事件要冪等
		if err := s.EnqueueWebhook(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	drain(t, s)

	convs, err := s.ListConversations(ctx, ListFilter{})
	if err != nil || len(convs) != 1 {
		t.Fatalf("convs=%v err=%v", convs, err)
	}
	c := convs[0]
	if c.UnreadCount != 1 || c.Contact.Username != "latte_lover" || c.LastMessageText != "請問今天有營業嗎" || c.WindowExpiresAt == nil {
		t.Fatalf("unexpected conversation: %+v", c)
	}
	if len(inbound) != 1 {
		t.Fatalf("OnInbound called %d times, want 1", len(inbound))
	}

	m, err := s.Send(ctx, c.ID, "有的，今天營業到晚上八點", "human")
	if err != nil {
		t.Fatal(err)
	}
	// 我方送出後 IG 會回一個 echo（同 mid）→ 不可重複落庫
	if err := s.EnqueueWebhook(ctx, payload(t, "acct", "u1", midOf(t, s, m.ID), "有的，今天營業到晚上八點", true, time.Now())); err != nil {
		t.Fatal(err)
	}
	// 從 IG App 直接回的 echo → 落庫為 human
	if err := s.EnqueueWebhook(ctx, payload(t, "acct", "u1", "m-app", "（店員手機回覆）", true, time.Now())); err != nil {
		t.Fatal(err)
	}
	drain(t, s)

	msgs, err := s.ListMessages(ctx, c.ID, 0, 50)
	if err != nil || len(msgs) != 3 {
		t.Fatalf("msgs=%d err=%v", len(msgs), err)
	}
	if msgs[1].SenderType != "human" || msgs[2].Direction != "out" {
		t.Fatalf("unexpected messages: %+v", msgs)
	}
	c, _ = s.GetConversation(ctx, c.ID)
	if c.Handler != "human" || c.UnreadCount != 0 {
		t.Fatalf("after human send: handler=%s unread=%d", c.Handler, c.UnreadCount)
	}
	if len(inbound) != 1 {
		t.Fatalf("echo must not trigger OnInbound")
	}
}

func TestSendWindowClosed(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	old := time.Now().Add(-30 * time.Hour)
	if err := s.EnqueueWebhook(ctx, payload(t, "u2", "acct", "m-old", "hi", false, old)); err != nil {
		t.Fatal(err)
	}
	drain(t, s)
	convs, _ := s.ListConversations(ctx, ListFilter{})
	if _, err := s.Send(ctx, convs[0].ID, "auto", "ai"); !errors.Is(err, ErrWindowClosed) {
		t.Fatalf("ai after 24h: err=%v", err)
	}
	if _, err := s.Send(ctx, convs[0].ID, "真人回覆", "human"); err != nil {
		t.Fatalf("human within 7d should pass with tag: %v", err)
	}
}

func midOf(t *testing.T, s *Service, id int64) string {
	var mid string
	if err := s.DB.QueryRow(context.Background(), `SELECT ig_mid FROM messages WHERE id = $1`, id).Scan(&mid); err != nil {
		t.Fatal(err)
	}
	return mid
}
