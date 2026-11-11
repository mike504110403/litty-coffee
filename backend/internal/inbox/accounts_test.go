package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mike504110403/litty-coffee/backend/internal/realtime"
	"github.com/mike504110403/litty-coffee/backend/internal/testdb"
)

// entryPayload 是送給我方帳號 accountIGID 的一則顧客訊息。
func entryPayload(t *testing.T, accountIGID, sender, mid, text string) []byte {
	b, err := json.Marshal(map[string]any{"object": "instagram", "entry": []any{map[string]any{
		"id": accountIGID, "time": time.Now().UnixMilli(),
		"messaging": []any{map[string]any{"sender": map[string]string{"id": sender}, "recipient": map[string]string{"id": accountIGID},
			"timestamp": time.Now().UnixMilli(), "message": map[string]any{"mid": mid, "text": text}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func eventErrors(t *testing.T, s *Service) []string {
	rows, err := s.DB.Query(context.Background(), `SELECT error FROM webhook_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

// 未連結的帳號（不存在、停用、沒掛連線）的 webhook 一律拒收：不建帳號、不建顧客，事件記「未連結的帳號」。
func TestWebhookForUnconnectedAccountRejected(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	// 帳號列存在但沒有連線（例如斷開後）
	if _, err := s.DB.Exec(ctx, `INSERT INTO ig_accounts (ig_user_id) VALUES ('detached')`); err != nil {
		t.Fatal(err)
	}
	// 已連結但停用
	off := testdb.ConnectedAccount(t, s.DB, "disabled-acct")
	if _, err := s.DB.Exec(ctx, `UPDATE ig_accounts SET active = false WHERE id = $1`, off); err != nil {
		t.Fatal(err)
	}
	enqueue(t, s,
		entryPayload(t, "stranger", "u1", "x1", "hi"),
		entryPayload(t, "detached", "u1", "x2", "hi"),
		entryPayload(t, "disabled-acct", "u1", "x3", "hi"),
		entryPayload(t, "acct", "u1", "x4", "hi"),
	)
	errs := eventErrors(t, s)
	want := []string{UnconnectedAccount, UnconnectedAccount, UnconnectedAccount, ""}
	for i := range want {
		if errs[i] != want[i] {
			t.Fatalf("event errors = %q, want %q", errs, want)
		}
	}
	var accounts, contacts int
	_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM ig_accounts WHERE ig_user_id = 'stranger'`).Scan(&accounts)
	_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM contacts`).Scan(&contacts)
	if accounts != 0 || contacts != 1 {
		t.Fatalf("stranger accounts=%d contacts=%d (want 0, 1)", accounts, contacts)
	}
}

type stubCreds map[int64][2]string

func (c stubCreds) Credentials(_ context.Context, id int64) (string, string, error) {
	v, ok := c[id]
	if !ok {
		return "", "", errors.New("no token")
	}
	return v[0], v[1], nil
}

// 兩個帳號的訊息進同一個收件匣，各自歸屬；回覆從對話所屬帳號、用該帳號的 token 送出；事件帶 AccountID。
func TestTwoAccountsMergedInboxAndReplyFromOwnAccount(t *testing.T) {
	s, fake := newTestService(t)
	ctx := context.Background()
	var a1 int64
	_ = s.DB.QueryRow(ctx, `SELECT id FROM ig_accounts WHERE ig_user_id = 'acct'`).Scan(&a1)
	a2 := testdb.ConnectedAccount(t, s.DB, "acct-2")
	s.Creds = stubCreds{a1: {"acct", "tok-1"}, a2: {"acct-2", "tok-2"}}

	hub := realtime.NewHub()
	s.Hub = hub
	ch, unsub := hub.Subscribe()
	defer unsub()

	enqueue(t, s, entryPayload(t, "acct", "u1", "m-a", "給一號店"), entryPayload(t, "acct-2", "u1", "m-b", "給二號店"))

	all, err := s.ListConversations(ctx, ListFilter{})
	if err != nil || len(all) != 2 {
		t.Fatalf("merged inbox: %d convs, err=%v", len(all), err)
	}
	only2, _ := s.ListConversations(ctx, ListFilter{Accounts: []int64{a2}})
	if len(only2) != 1 || only2[0].IGAccountID != a2 || only2[0].LastMessageText != "給二號店" {
		t.Fatalf("filter by account: %+v", only2)
	}

	if _, err := s.Send(ctx, only2[0].ID, "二號店回覆", "human"); err != nil {
		t.Fatal(err)
	}
	sent := fake.Sent()
	last := sent[len(sent)-1]
	if last.AccountID != "acct-2" || last.Token != "tok-2" {
		t.Fatalf("reply sent from %s with %s, want acct-2/tok-2", last.AccountID, last.Token)
	}
	msgs, _ := s.ListMessages(ctx, only2[0].ID, 0, 10)
	for _, m := range msgs {
		if m.IGAccountID != a2 {
			t.Fatalf("message %d igAccountId=%d", m.ID, m.IGAccountID)
		}
	}

	seen := map[int64]bool{}
	timeout := time.After(time.Second)
	for len(seen) < 2 {
		select {
		case e := <-ch:
			if e.AccountID == 0 {
				t.Fatalf("event %s without AccountID", e.Type)
			}
			seen[e.AccountID] = true
		case <-timeout:
			t.Fatalf("events seen for accounts %v", seen)
		}
	}

	// 停用的帳號不能送
	s.Creds = nil
	if _, err := s.DB.Exec(ctx, `UPDATE ig_accounts SET active = false WHERE id = $1`, a2); err != nil {
		t.Fatal(err)
	}
	s.Creds = inactiveCreds{}
	if _, err := s.Send(ctx, only2[0].ID, "x", "human"); !errors.Is(err, ErrCannotSend) {
		t.Fatalf("send from disabled account: %v", err)
	}
}

type inactiveCreds struct{}

func (inactiveCreds) Credentials(context.Context, int64) (string, string, error) {
	return "", "", errors.New("這個 IG 帳號已停用")
}
