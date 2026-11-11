package inbox

import (
	"context"
	"testing"
	"time"
)

// 實測：graph.facebook.com 送出回傳的 mid 與 echo 的 mid 格式不同，要靠「同對話、同內容、前後 2 分鐘」配對，
// 不能讓同一則回覆出現兩次（一筆 admin、一筆「真人」）。
func TestEchoWithDifferentMidIsMerged(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	var adminID int64
	if err := s.DB.QueryRow(ctx, `INSERT INTO admin_users (username, password_hash, display_name)
VALUES ('echo-test', 'x', '阿明') RETURNING id`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// 先解除訊息的參照，否則外鍵擋住刪除，下次跑會撞名
		_, _ = s.DB.Exec(context.Background(), `UPDATE messages SET sent_by_admin_id = NULL WHERE sent_by_admin_id = $1`, adminID)
		_, _ = s.DB.Exec(context.Background(), `DELETE FROM admin_users WHERE id = $1`, adminID)
	})
	enqueue(t, s, payload(t, "u1", "acct", "in-1", "嗨", false, time.Now()))
	convs, _ := s.ListConversations(ctx, ListFilter{})
	convID := convs[0].ID

	// 1) 後台先送出，echo 後到（mid 不同）→ 不新增
	if _, err := s.SendAsAdmin(ctx, convID, "你好", adminID); err != nil {
		t.Fatal(err)
	}
	enqueue(t, s, payload(t, "acct", "u1", "echo-a", "你好", true, time.Now()))
	enqueue(t, s, payload(t, "acct", "u1", "echo-a", "你好", true, time.Now())) // 同一個 echo 重送

	// 2) echo 先到（以「真人」落庫），後台送出後認領成自己的
	enqueue(t, s, payload(t, "acct", "u1", "echo-b", "ㄅ", true, time.Now()))
	m, err := s.SendAsAdmin(ctx, convID, "ㄅ", adminID)
	if err != nil {
		t.Fatal(err)
	}
	if m.SentBy == nil || m.SentBy.DisplayName != "阿明" {
		t.Fatalf("claimed echo should carry sender: %+v", m)
	}

	// 3) 店員真的從 IG App 回了不同內容 → 照常新增為「真人」
	enqueue(t, s, payload(t, "acct", "u1", "echo-c", "稍等喔", true, time.Now()))

	msgs, _ := s.ListMessages(ctx, convID, 0, 50)
	if len(msgs) != 4 {
		for _, x := range msgs {
			t.Logf("%d %s %s %+v", x.ID, x.SenderType, x.Text, x.SentBy)
		}
		t.Fatalf("want 4 messages (嗨, 你好, ㄅ, 稍等喔), got %d", len(msgs))
	}
	if msgs[1].SentBy == nil || msgs[2].SentBy == nil || msgs[3].SentBy != nil {
		t.Fatalf("senders: %+v %+v %+v", msgs[1].SentBy, msgs[2].SentBy, msgs[3].SentBy)
	}
}
