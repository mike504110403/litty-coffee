package automation

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mike504110403/litty-coffee/backend/internal/inbox"
)

// 自動回覆總開關關閉：訊息照收，但規則、AI、降頻、轉真人通知、待改進都不發生；打開後恢復正常。
func TestAutoReplySwitchOff(t *testing.T) {
	e, ai, acc := setup(t)
	ctx := context.Background()
	if _, err := e.Store.CreateRule(ctx, acc, Rule{Name: "客訴", MatchType: "contains", Keywords: []string{"退錢"}, Reply: "抱歉", Handoff: true, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	ai.d = AIDecision{Action: "handoff", Reason: "不確定", Intent: "other"}
	st, err := e.Store.GetSettings(ctx, acc)
	if err != nil {
		t.Fatal(err)
	}
	if !st.AutoReplyEnabled || st.DigestTime != DefaultDigestTime {
		t.Fatalf("新帳號預設應開啟、09:00：%+v", st)
	}
	st.AutoReplyEnabled = false
	st.SpamMaxMsgs, st.SpamWindowSecs = 1, 300 // 第二則就會觸發降頻（如果有跑的話）
	if st, err = e.Store.SaveSettings(ctx, acc, st); err != nil {
		t.Fatal(err)
	}
	events, unsub := e.Inbox.Hub.Subscribe()
	defer unsub()

	var conv int64
	for i, text := range []string{"我要退錢", "在嗎", "還在嗎"} { // 規則命中、會走 AI 的都不處理
		c, m := customerSays(t, e, "u1", fmt.Sprintf("m%d", i), text)
		conv = c
		if err := e.handle(ctx, c, m.ID, st); err != nil {
			t.Fatal(err)
		}
		e.OnInbound(c, m) // 背景路徑也直接略過
	}
	time.Sleep(100 * time.Millisecond)
	if n := outboundCount(t, e, conv); n != 0 || ai.calls != 0 {
		t.Fatalf("switch off: %d replies, ai calls %d", n, ai.calls)
	}
	evs := eventsOf(events)
	if hasEvent(evs, "handoff.requested") {
		t.Fatal("switch off must not publish handoff.requested")
	}
	if !hasEvent(evs, "message.created") {
		t.Fatal("messages should still be received")
	}
	c, _ := e.Inbox.GetConversation(ctx, conv)
	if c.Handler != "bot" || c.Contact.Risk != "" || c.Contact.ThrottleLevel != 0 {
		t.Fatalf("switch off should not hand off / throttle / flag: %+v", c)
	}
	if got := reviewsOf(t, e); len(got) != 0 {
		t.Fatalf("switch off should not create reviews: %+v", got)
	}
	var hits int64
	_ = e.Store.DB.QueryRow(ctx, `SELECT COALESCE(sum(hit_count), 0) FROM auto_rules WHERE ig_account_id = $1`, acc).Scan(&hits)
	if hits != 0 {
		t.Fatalf("rule hits = %d", hits)
	}

	// 打開後同一段對話恢復自動回覆（AI 轉真人 → 送轉真人訊息＋推 handoff.requested）
	st.AutoReplyEnabled = true
	st.SpamMaxMsgs = 0
	if st, err = e.Store.SaveSettings(ctx, acc, st); err != nil {
		t.Fatal(err)
	}
	c2, m := customerSays(t, e, "u1", "m-on", "在嗎")
	if err := e.handle(ctx, c2, m.ID, st); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 1 || outboundCount(t, e, c2) != 1 || !hasEvent(eventsOf(events), "handoff.requested") {
		t.Fatalf("switch on: ai calls %d, outbound %d", ai.calls, outboundCount(t, e, c2))
	}
}

// 範本的開關與摘要時間會複製給新帳號；摘要時間格式要驗證。
func TestDigestTimeSetting(t *testing.T) {
	e, _, _ := setup(t)
	ctx := context.Background()
	if _, err := e.Store.DB.Exec(ctx, `UPDATE automation_settings SET auto_reply_enabled = false, digest_time = '07:30' WHERE ig_account_id IS NULL`); err != nil {
		t.Fatal(err)
	}
	var acc2 int64
	if err := e.Store.DB.QueryRow(ctx, `INSERT INTO ig_accounts (ig_user_id) VALUES ('acct2') RETURNING id`).Scan(&acc2); err != nil {
		t.Fatal(err)
	}
	st, err := e.Store.GetSettings(ctx, acc2)
	if err != nil {
		t.Fatal(err)
	}
	if st.AutoReplyEnabled || st.DigestTime != "07:30" {
		t.Fatalf("copied from template: %+v", st)
	}
	for _, bad := range []string{"9:00", "24:00", "12:60", "abc"} {
		st.DigestTime = bad
		if _, err := e.Store.SaveSettings(ctx, acc2, st); err == nil {
			t.Errorf("digest time %q should be rejected", bad)
		}
	}
	st.DigestTime = ""
	if st, err = e.Store.SaveSettings(ctx, acc2, st); err != nil || st.DigestTime != DefaultDigestTime {
		t.Fatalf("empty digest time → default: %+v %v", st, err)
	}
}

// 引擎記下真的打出去的 AI 呼叫（系統狀態用）：失敗累計、成功歸零；模型拒答算成功。
func TestEngineRecordsAICalls(t *testing.T) {
	e, ai, acc := setup(t)
	ctx := context.Background()
	e.Calls = &CallStats{}
	st, _ := e.Store.GetSettings(ctx, acc)
	_, m := customerSays(t, e, "u1", "m1", "在嗎")
	hist := []inbox.Message{m}
	ai.err = errors.New("boom")
	for i := 0; i < 2; i++ {
		if _, err := e.Evaluate(ctx, acc, st, hist); err == nil {
			t.Fatal("want error")
		}
	}
	last, fails := e.Calls.Last()
	if last == nil || last.OK || last.Kind != "reply" || last.Error != "boom" || fails != 2 {
		t.Fatalf("after failures: %+v fails=%d", last, fails)
	}
	ai.err = ErrRefused
	if _, err := e.Evaluate(ctx, acc, st, hist); err != nil {
		t.Fatal(err)
	}
	if last, fails = e.Calls.Last(); !last.OK || fails != 0 {
		t.Fatalf("refusal counts as a successful call: %+v fails=%d", last, fails)
	}
	// 沒設 key（LiveAI 未啟用）不算呼叫
	e2 := &Engine{Store: e.Store, Inbox: e.Inbox, AI: &LiveAI{}, Calls: &CallStats{}}
	if _, err := e2.Evaluate(ctx, acc, st, hist); err != nil {
		t.Fatal(err)
	}
	if last, _ := e2.Calls.Last(); last != nil {
		t.Fatalf("disabled AI should not be recorded: %+v", last)
	}
	var nilStats *CallStats
	nilStats.Record("reply", time.Now(), nil) // nil 接收者不 panic
}
