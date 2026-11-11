package automation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mike504110403/litty-coffee/backend/internal/ig"
	"github.com/mike504110403/litty-coffee/backend/internal/inbox"
)

// backdate 把一則訊息（與對話的 last_inbound_at）改成 d 之前。
func backdate(t *testing.T, e *Engine, msgID int64, d time.Duration) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.Store.DB.Exec(ctx, `UPDATE messages SET created_at = now() - make_interval(secs => $2) WHERE id = $1`, msgID, d.Seconds()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.DB.Exec(ctx, `
UPDATE conversations c SET last_inbound_at = (SELECT max(created_at) FROM messages WHERE conversation_id = c.id AND direction = 'in')
WHERE id = (SELECT conversation_id FROM messages WHERE id = $1)`, msgID); err != nil {
		t.Fatal(err)
	}
}

// humanWaiting：顧客傳一則、對話在真人手上，且這則已是 d 之前。
func humanWaiting(t *testing.T, e *Engine, igsid, mid, text string, d time.Duration) (int64, inbox.Message) {
	t.Helper()
	conv, m := customerSays(t, e, igsid, mid, text)
	if err := e.Inbox.SetHandler(context.Background(), conv, "human"); err != nil {
		t.Fatal(err)
	}
	backdate(t, e, m.ID, d)
	return conv, m
}

func idleOnce(t *testing.T, e *Engine) int {
	t.Helper()
	n, err := e.IdleHandbackOnce(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func handlerOf(t *testing.T, e *Engine, conv int64) string {
	t.Helper()
	c, err := e.Inbox.GetConversation(context.Background(), conv)
	if err != nil {
		t.Fatal(err)
	}
	return c.Handler
}

func messagesOf(t *testing.T, e *Engine, conv int64) []inbox.Message {
	t.Helper()
	msgs, err := e.Inbox.ListMessages(context.Background(), conv, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}

func countSender(msgs []inbox.Message, sender string) int {
	n := 0
	for _, m := range msgs {
		if m.SenderType == sender {
			n++
		}
	}
	return n
}

// 到時間：切回 bot、寫系統事件（推 conversation.updated／message.created）、AI 帶「久候」提示回覆那則；對話紀錄不含系統事件。
func TestIdleHandbackRepliesWithApology(t *testing.T) {
	e, ai, _ := setup(t)
	ai.d = AIDecision{Action: "reply", Reply: "久等了，不好意思～我們 10 點開", Intent: "hours"}
	conv, m := humanWaiting(t, e, "u1", "m1", "你們幾點開？", 14*time.Minute)
	if n := idleOnce(t, e); n != 0 || handlerOf(t, e, conv) != "human" {
		t.Fatal("未滿 15 分鐘不交回")
	}
	backdate(t, e, m.ID, 16*time.Minute)
	events, unsub := e.Inbox.Hub.Subscribe()
	defer unsub()
	if n := idleOnce(t, e); n != 1 {
		t.Fatalf("handed back %d", n)
	}
	if handlerOf(t, e, conv) != "bot" {
		t.Fatal("應切回 bot")
	}
	msgs := messagesOf(t, e, conv)
	if len(msgs) != 3 {
		t.Fatalf("messages: %+v", msgs)
	}
	sys, reply := msgs[1], msgs[2]
	if sys.SenderType != "system" || sys.Kind != "system" || sys.Direction != "out" || sys.Text != "真人 15 分鐘未回覆，已交回 AI" {
		t.Fatalf("system message: %+v", sys)
	}
	if reply.SenderType != "ai" || reply.Text != ai.d.Reply {
		t.Fatalf("reply: %+v", reply)
	}
	if sent := e.Inbox.IG.(*ig.Fake).Sent(); len(sent) != 1 || sent[0].Text != ai.d.Reply {
		t.Fatal("只有 AI 回覆送 IG，系統事件不送")
	}
	if ai.calls != 1 || ai.waited < 15 || ai.waited > 17 {
		t.Fatalf("AI 應收到久候分鐘數：calls %d waited %d", ai.calls, ai.waited)
	}
	if len(ai.turns) != 1 || ai.turns[0].Text != "你們幾點開？" {
		t.Fatalf("系統事件不得進 AI 對話紀錄：%+v", ai.turns)
	}
	if p := systemPrompt(Settings{WaitedMinutes: 16}, nil); !strings.Contains(p, "約 16 分鐘") || !strings.Contains(p, "久等了") ||
		strings.Contains(systemPrompt(Settings{}, nil), "久候") {
		t.Fatal("prompt 應要求開頭致歉")
	}
	evs := eventsOf(events)
	var sawConvBot, sawSys bool
	for _, ev := range evs {
		if c, ok := ev.Data.(inbox.Conversation); ok && ev.Type == "conversation.updated" && c.Handler == "bot" {
			sawConvBot = true
		}
		if mm, ok := ev.Data.(inbox.Message); ok && ev.Type == "message.created" && mm.SenderType == "system" {
			sawSys = true
		}
	}
	if !sawConvBot || !sawSys {
		t.Fatalf("events: %+v", evs)
	}
	// 已處理過：不會重複
	if n := idleOnce(t, e); n != 0 {
		t.Fatal("同一則不重複交回")
	}
	c, _ := e.Inbox.GetConversation(context.Background(), conv)
	if c.LastMessageText != ai.d.Reply {
		t.Fatalf("系統事件不改對話的最後訊息：%q", c.LastMessageText)
	}
}

// 真人回過就從下一則顧客訊息重新計時。
func TestIdleHandbackRestartsAfterHumanReply(t *testing.T) {
	e, ai, _ := setup(t)
	ctx := context.Background()
	ai.d = AIDecision{Action: "reply", Reply: "久等了～", Intent: "other"}
	conv, m1 := humanWaiting(t, e, "u1", "m1", "第一題", 40*time.Minute)
	h, err := e.Inbox.Send(ctx, conv, "店員回了第一題", "human")
	if err != nil {
		t.Fatal(err)
	}
	backdate(t, e, h.ID, 30*time.Minute)
	if n := idleOnce(t, e); n != 0 {
		t.Fatal("真人已回、沒有新的顧客訊息：不交回")
	}
	_, m2 := customerSays(t, e, "u1", "m2", "第二題")
	backdate(t, e, m2.ID, 10*time.Minute) // 第一題雖然 40 分鐘前，但計時從第二題起算
	if n := idleOnce(t, e); n != 0 {
		t.Fatal("第二題才 10 分鐘：不交回")
	}
	backdate(t, e, m2.ID, 15*time.Minute)
	if n := idleOnce(t, e); n != 1 || handlerOf(t, e, conv) != "bot" {
		t.Fatal("第二題滿 15 分鐘：交回")
	}
	_ = m1
}

// AI 交回後又轉真人：不再送轉真人訊息、留在 human；同一則不再觸發，要等下一則新的顧客訊息（持久化，重啟後也成立）。
func TestIdleHandbackHandoffAgain(t *testing.T) {
	e, ai, _ := setup(t)
	ctx := context.Background()
	ai.d = AIDecision{Action: "handoff", Intent: "reservation", Reason: "要確認訂位"}
	conv, m1 := humanWaiting(t, e, "u1", "m1", "幫我訂位", 20*time.Minute)
	events, unsub := e.Inbox.Hub.Subscribe()
	defer unsub()
	if n := idleOnce(t, e); n != 1 {
		t.Fatalf("handed back %d", n)
	}
	if handlerOf(t, e, conv) != "human" {
		t.Fatal("AI 又轉真人：對話留在 human")
	}
	msgs := messagesOf(t, e, conv)
	if countSender(msgs, "bot") != 0 || countSender(msgs, "system") != 1 || len(e.Inbox.IG.(*ig.Fake).Sent()) != 0 {
		t.Fatalf("不得再送轉真人訊息：%+v", msgs)
	}
	if !hasEvent(eventsOf(events), "handoff.requested") {
		t.Fatal("仍要通知店員")
	}
	var marked int64
	if err := e.Store.DB.QueryRow(ctx, `SELECT idle_handback_msg_id FROM conversations WHERE id = $1`, conv).Scan(&marked); err != nil || marked != m1.ID {
		t.Fatalf("idle_handback_msg_id = %d %v", marked, err)
	}
	// 再掃（模擬重啟後的新引擎）：同一則不再觸發
	e2 := &Engine{Store: e.Store, Inbox: e.Inbox, AI: ai, Ctx: ctx}
	backdate(t, e, m1.ID, 3*time.Hour)
	if n, err := e2.IdleHandbackOnce(ctx, time.Now()); err != nil || n != 0 || ai.calls != 1 {
		t.Fatalf("同一則不重複：n=%d calls=%d err=%v", n, ai.calls, err)
	}
	// 下一則新的顧客訊息：重新計時、到時間再交回
	_, m2 := customerSays(t, e, "u1", "m2", "還在嗎")
	backdate(t, e, m2.ID, 16*time.Minute)
	ai.d = AIDecision{Action: "reply", Reply: "久等了～在的", Intent: "other"}
	if n := idleOnce(t, e); n != 1 || handlerOf(t, e, conv) != "bot" || ai.calls != 2 {
		t.Fatalf("新訊息應再交回：handler %s calls %d", handlerOf(t, e, conv), ai.calls)
	}
}

// 不觸發：黑名單、自動回覆關、AI 回覆關、超過 IG 24 小時窗口、設定 0（關）、沒有 AI key、真人在處理但對話是 bot。
func TestIdleHandbackSkips(t *testing.T) {
	cases := map[string]func(t *testing.T, e *Engine, acc, conv int64){
		"blocked": func(t *testing.T, e *Engine, _, conv int64) {
			if _, err := e.Store.DB.Exec(context.Background(), `UPDATE contacts SET risk = 'blocked'`); err != nil {
				t.Fatal(err)
			}
		},
		"auto reply off": func(t *testing.T, e *Engine, acc, _ int64) {
			setAutoReply(t, e, acc, func(s *Settings) { s.AutoReplyEnabled = false })
		},
		"ai off": func(t *testing.T, e *Engine, acc, _ int64) {
			setAutoReply(t, e, acc, func(s *Settings) { s.AIEnabled = false })
		},
		"idle 0 = off": func(t *testing.T, e *Engine, acc, _ int64) {
			setAutoReply(t, e, acc, func(s *Settings) { s.HumanIdleMinutes = 0 })
		},
		"outside 24h window": func(t *testing.T, e *Engine, _, conv int64) {
			var id int64
			if err := e.Store.DB.QueryRow(context.Background(), `SELECT id FROM messages WHERE conversation_id = $1`, conv).Scan(&id); err != nil {
				t.Fatal(err)
			}
			backdate(t, e, id, 25*time.Hour)
		},
		"no AI key": func(t *testing.T, e *Engine, _, _ int64) {
			e.AI = &LiveAI{}
		},
		"handler bot": func(t *testing.T, e *Engine, _, conv int64) {
			if err := e.Inbox.SetHandler(context.Background(), conv, "bot"); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, prep := range cases {
		t.Run(name, func(t *testing.T) {
			e, ai, acc := setup(t)
			ai.d = AIDecision{Action: "reply", Reply: "嗨", Intent: "other"}
			conv, _ := humanWaiting(t, e, "u1", "m1", "在嗎", 30*time.Minute)
			prep(t, e, acc, conv)
			want := handlerOf(t, e, conv)
			if n := idleOnce(t, e); n != 0 || ai.calls != 0 || handlerOf(t, e, conv) != want {
				t.Fatalf("不該交回：n=%d calls=%d", n, ai.calls)
			}
			if countSender(messagesOf(t, e, conv), "system") != 0 {
				t.Fatal("不該寫系統事件")
			}
		})
	}
	t.Run("other minutes", func(t *testing.T) {
		e, ai, acc := setup(t)
		ai.d = AIDecision{Action: "reply", Reply: "嗨", Intent: "other"}
		setAutoReply(t, e, acc, func(s *Settings) { s.HumanIdleMinutes = 30 })
		conv, m := humanWaiting(t, e, "u1", "m1", "在嗎", 20*time.Minute)
		if idleOnce(t, e) != 0 {
			t.Fatal("設 30 分：20 分不交回")
		}
		backdate(t, e, m.ID, 31*time.Minute)
		if idleOnce(t, e) != 1 || messagesOf(t, e, conv)[1].Text != "真人 30 分鐘未回覆，已交回 AI" {
			t.Fatalf("30 分交回：%+v", messagesOf(t, e, conv))
		}
	})
}

// 交回後規則與 AI 都沒處理（例：圖片）：還給真人，不留在 bot。
func TestIdleHandbackNoneRevertsToHuman(t *testing.T) {
	e, ai, _ := setup(t)
	ctx := context.Background()
	conv, m := humanWaiting(t, e, "u1", "m1", "x", 20*time.Minute)
	if _, err := e.Store.DB.Exec(ctx, `UPDATE messages SET kind = 'image', text = '' WHERE id = $1`, m.ID); err != nil {
		t.Fatal(err)
	}
	if n := idleOnce(t, e); n != 1 {
		t.Fatalf("n=%d", n)
	}
	if handlerOf(t, e, conv) != "human" || ai.calls != 0 {
		t.Fatalf("handler %s calls %d", handlerOf(t, e, conv), ai.calls)
	}
	if idleOnce(t, e) != 0 {
		t.Fatal("同一則不重複")
	}
}

// 交回後 AI 轉真人時，背景建議照常排程（同一則已有就不重產生）。
func TestIdleHandbackSchedulesSuggestion(t *testing.T) {
	e, ai, fs, _ := setupBG(t)
	ai.d = AIDecision{Action: "handoff", Intent: "other", Reason: "不確定"}
	conv, m := humanWaiting(t, e, "u1", "m1", "這個問題", 20*time.Minute)
	if idleOnce(t, e) != 1 {
		t.Fatal("應交回")
	}
	settle(e)
	if v := current(t, e, conv); fs.count() != 1 || v.Status != SuggestionReady || v.ForMessageID != m.ID {
		t.Fatalf("calls %d view %+v", fs.count(), v)
	}
	if len(fs.turns) != 1 {
		t.Fatalf("建議的對話紀錄不含系統事件：%+v", fs.turns)
	}
}

// 蒸餾素材不含系統事件訊息（不會被當成店家回答）。
func TestDistillSourceSkipsSystemMessages(t *testing.T) {
	e, _, acc := setup(t)
	ctx := context.Background()
	conv, _ := customerSays(t, e, "u1", "m1", "有燕麥奶嗎")
	if _, err := e.Inbox.AddSystemMessage(ctx, conv, "真人 15 分鐘未回覆，已交回 AI"); err != nil {
		t.Fatal(err)
	}
	customerSays(t, e, "u1", "m2", "要加錢嗎") // 系統事件若被當成 AI 回覆，前一個問題會被切掉
	if _, err := e.Inbox.Send(ctx, conv, "有的，加 20 元", "human"); err != nil {
		t.Fatal(err)
	}
	src, err := e.Store.LoadDistillSource(ctx, acc, 7, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(src.Pairs) != 1 || !strings.Contains(src.Pairs[0].Question, "有燕麥奶嗎") || !strings.Contains(src.Pairs[0].Question, "要加錢嗎") ||
		src.Pairs[0].Answer != "有的，加 20 元" {
		t.Fatalf("pairs: %+v", src.Pairs)
	}
}
