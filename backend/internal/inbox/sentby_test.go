package inbox

import (
	"context"
	"testing"
	"time"

	"github.com/mike504110403/litty-coffee/backend/internal/ig"
)

// 後台真人回覆記下送出的管理員；自動回覆與從 IG app 回的 echo 沒有送出者。
func TestSendAsAdminRecordsSender(t *testing.T) {
	s, fake := newTestService(t)
	ctx := context.Background()
	fake.Register("u1", ig.Profile{Username: "latte_lover"})
	var adminID int64
	if err := s.DB.QueryRow(ctx, `INSERT INTO admin_users (username, password_hash, display_name)
VALUES ('sentby-test-mei', 'x', '小美') RETURNING id`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// 先解除訊息的參照，否則外鍵擋住刪除，下次跑會撞名
		_, _ = s.DB.Exec(context.Background(), `UPDATE messages SET sent_by_admin_id = NULL WHERE sent_by_admin_id = $1`, adminID)
		_, _ = s.DB.Exec(context.Background(), `DELETE FROM admin_users WHERE id = $1`, adminID)
	})

	if err := s.EnqueueWebhook(ctx, payload(t, "u1", "acct", "m1", "hi", false, time.Now())); err != nil {
		t.Fatal(err)
	}
	drain(t, s)
	convs, _ := s.ListConversations(ctx, ListFilter{})
	convID := convs[0].ID

	m, err := s.SendAsAdmin(ctx, convID, "您好", adminID)
	if err != nil {
		t.Fatal(err)
	}
	if m.SenderType != "human" || m.SentBy == nil || m.SentBy.ID != adminID || m.SentBy.DisplayName != "小美" {
		t.Fatalf("returned message sentBy = %+v (sender %s)", m.SentBy, m.SenderType)
	}
	// 我方送出的 echo 晚到：不能把送出者洗掉
	if err := s.EnqueueWebhook(ctx, payload(t, "acct", "u1", midOf(t, s, m.ID), "您好", true, time.Now())); err != nil {
		t.Fatal(err)
	}
	// 店員從 IG app 直接回
	if err := s.EnqueueWebhook(ctx, payload(t, "acct", "u1", "m-app", "手機回覆", true, time.Now())); err != nil {
		t.Fatal(err)
	}
	drain(t, s)
	if _, err := s.Send(ctx, convID, "自動回覆", "bot"); err != nil {
		t.Fatal(err)
	}

	msgs, err := s.ListMessages(ctx, convID, 0, 50)
	if err != nil || len(msgs) != 4 {
		t.Fatalf("msgs=%d err=%v", len(msgs), err)
	}
	if msgs[1].SentBy == nil || msgs[1].SentBy.DisplayName != "小美" {
		t.Fatalf("admin reply sentBy = %+v", msgs[1].SentBy)
	}
	for _, i := range []int{0, 2, 3} {
		if msgs[i].SentBy != nil {
			t.Fatalf("msg %d (%s %q) should have no sentBy, got %+v", i, msgs[i].SenderType, msgs[i].Text, msgs[i].SentBy)
		}
	}
	got, err := s.GetMessage(ctx, m.ID)
	if err != nil || got.SentBy == nil || got.SentBy.ID != adminID {
		t.Fatalf("GetMessage sentBy = %+v err=%v", got.SentBy, err)
	}
}
