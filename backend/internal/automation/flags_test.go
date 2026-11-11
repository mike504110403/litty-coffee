package automation

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mike504110403/litty-coffee/backend/internal/inbox"
	"github.com/mike504110403/litty-coffee/backend/internal/realtime"
)

// drain 丟掉目前累積的事件。
func drain(ch <-chan realtime.Event) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// eventsOf 取出目前累積的事件（不等待）。
func eventsOf(ch <-chan realtime.Event) []realtime.Event {
	var out []realtime.Event
	for {
		select {
		case ev := <-ch:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func hasEvent(evs []realtime.Event, typ string) bool {
	for _, ev := range evs {
		if ev.Type == typ {
			return true
		}
	}
	return false
}

func outboundCount(t *testing.T, e *Engine, conv int64) int {
	t.Helper()
	var n int
	if err := e.Inbox.DB.QueryRow(context.Background(), `SELECT count(*) FROM messages WHERE conversation_id = $1 AND direction = 'out'`, conv).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

type riskRow struct{ risk, reason, source string }

func contactRisk(t *testing.T, e *Engine, conv int64) riskRow {
	t.Helper()
	c, err := e.Inbox.GetConversation(context.Background(), conv)
	if err != nil {
		t.Fatal(err)
	}
	return riskRow{c.Contact.Risk, c.Contact.RiskReason, c.Contact.RiskSource}
}

func reviewsOf(t *testing.T, e *Engine) []Review {
	t.Helper()
	list, err := e.Store.ListReviews(context.Background(), ReviewFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// 黑名單顧客：訊息照收，但規則、AI 都不跑，不回覆、不發轉真人通知。
func TestBlockedContactGetsNoReplyNoHandoff(t *testing.T) {
	e, ai, acc := setup(t)
	ctx := context.Background()
	st, _ := e.Store.GetSettings(ctx, acc)
	if _, err := e.Store.CreateRule(ctx, acc, Rule{Name: "客訴", MatchType: "contains", Keywords: []string{"退錢"}, Reply: "抱歉", Handoff: true, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	ai.d = AIDecision{Action: "handoff", Reason: "不確定"}
	conv, m := customerSays(t, e, "u1", "m1", "你好")
	c, _ := e.Inbox.GetConversation(ctx, conv)
	if err := e.Inbox.SetRisk(ctx, c.Contact.ID, inbox.RiskBlocked, "一直亂傳", 0); err != nil {
		t.Fatal(err)
	}
	events, unsub := e.Inbox.Hub.Subscribe()
	defer unsub()

	for i, text := range []string{"我要退錢", "在嗎"} { // 規則命中、會走 AI 的都不回
		conv, m = customerSays(t, e, "u1", fmt.Sprintf("m%d", i+2), text)
		if err := e.handle(ctx, conv, m.ID, st); err != nil {
			t.Fatal(err)
		}
	}
	if n := outboundCount(t, e, conv); n != 0 || ai.calls != 0 {
		t.Fatalf("blocked contact got %d replies, ai calls %d", n, ai.calls)
	}
	evs := eventsOf(events)
	if hasEvent(evs, "handoff.requested") {
		t.Fatal("blocked contact must not trigger handoff.requested")
	}
	if !hasEvent(evs, "message.created") {
		t.Fatal("blocked contact's messages should still be received")
	}
	if c, _ := e.Inbox.GetConversation(ctx, conv); c.Handler != "bot" || c.Contact.Risk != inbox.RiskBlocked {
		t.Fatalf("conversation after blocked messages: %+v", c)
	}
	if got := reviewsOf(t, e); len(got) != 0 {
		t.Fatalf("blocked contact should not create reviews: %+v", got)
	}

	// 就算有人直接呼叫 Handoff，黑名單也不推通知
	if err := e.Inbox.Handoff(ctx, conv, "x"); err != nil {
		t.Fatal(err)
	}
	if hasEvent(eventsOf(events), "handoff.requested") {
		t.Fatal("Handoff on blocked contact must not publish handoff.requested")
	}
}

func TestThrottleInterval(t *testing.T) {
	want := map[int]time.Duration{0: 0, 1: 5 * time.Minute, 2: 10 * time.Minute, 3: 20 * time.Minute, 4: 40 * time.Minute,
		7: 320 * time.Minute, 8: 6 * time.Hour, 50: 6 * time.Hour}
	for lv, d := range want {
		if got := ThrottleInterval(lv); got != d {
			t.Errorf("level %d: %v, want %v", lv, got, d)
		}
	}
}

// ago 把對話裡符合條件的訊息往前挪（模擬時間經過）。
func ago(t *testing.T, e *Engine, conv int64, cond string, d time.Duration) {
	t.Helper()
	if _, err := e.Inbox.DB.Exec(context.Background(),
		`UPDATE messages SET created_at = created_at - make_interval(secs => $2) WHERE conversation_id = $1 AND `+cond, conv, d.Seconds()); err != nil {
		t.Fatal(err)
	}
}

// 短時間大量訊息 → 不封鎖，改標問題顧客（來源 rate）＋降頻：間隔內只收不回、每回一次間隔加倍（5→10→20 分）、
// 顧客安靜超過目前間隔就歸零；降頻中 AI 收到「簡短委婉」旗標；該轉真人照樣轉、照樣通知。
func TestRateThrottle(t *testing.T) {
	e, ai, acc := setup(t)
	ctx := context.Background()
	st, _ := e.Store.GetSettings(ctx, acc)
	if st.SpamMaxMsgs != 10 || st.SpamWindowSecs != 300 || !st.AIModeration {
		t.Fatalf("default spam settings: %+v", st)
	}
	st.SpamMaxMsgs = 3
	if _, err := e.Store.SaveSettings(ctx, acc, st); err != nil {
		t.Fatal(err)
	}
	st, _ = e.Store.GetSettings(ctx, acc)
	if _, err := e.Store.CreateRule(ctx, acc, Rule{Name: "嗨", MatchType: "contains", Keywords: []string{"嗨"}, Reply: "嗨嗨", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	ai.d = AIDecision{Action: "reply", Reply: "好的，店員稍後回覆你"}
	n := 0
	say := func(text string) int64 {
		n++
		conv, m := customerSays(t, e, "u1", fmt.Sprintf("m%d", n), text)
		if err := e.handle(ctx, conv, m.ID, st); err != nil {
			t.Fatal(err)
		}
		return conv
	}
	level := func(conv int64) int {
		c, _ := e.Inbox.GetConversation(ctx, conv)
		return c.Contact.ThrottleLevel
	}

	var conv int64
	for i := 0; i < 3; i++ { // 3 則＝沒超過
		conv = say("嗨")
	}
	if r := contactRisk(t, e, conv); r.risk != "" || outboundCount(t, e, conv) != 3 || level(conv) != 0 {
		t.Fatalf("3 messages should not throttle: %+v out=%d", r, outboundCount(t, e, conv))
	}

	events, unsub := e.Inbox.Hub.Subscribe()
	defer unsub()
	drain(events)
	conv = say("嗨") // 第 4 則：進入降頻；上次自動回覆才剛發生 → 只收不回
	r := contactRisk(t, e, conv)
	if r.risk != inbox.RiskWatch || r.source != inbox.RiskSourceRate || r.reason != "5 分鐘內傳了 4 則，已降低回覆頻率" || level(conv) != 1 {
		t.Fatalf("throttle start: %+v level=%d", r, level(conv))
	}
	if outboundCount(t, e, conv) != 3 {
		t.Fatalf("within interval should not reply: out=%d", outboundCount(t, e, conv))
	}
	evs := eventsOf(events)
	if hasEvent(evs, "handoff.requested") {
		t.Fatal("throttle should not hand off by itself")
	}
	var pushed *inbox.Conversation
	for _, ev := range evs {
		if ev.Type == "conversation.updated" {
			c := ev.Data.(inbox.Conversation)
			pushed = &c
		}
	}
	if pushed == nil || pushed.Contact.Risk != inbox.RiskWatch || pushed.Contact.RiskSource != inbox.RiskSourceRate {
		t.Fatalf("conversation.updated with watch: %+v", pushed)
	}
	say("嗨")
	if outboundCount(t, e, conv) != 3 {
		t.Fatal("still within 5 min: no reply")
	}

	// 5 分鐘過了 → 回一次（AI 走降頻模式）→ 升 2 級（10 分）
	ago(t, e, conv, "direction = 'out'", 6*time.Minute)
	say("還在嗎")
	if outboundCount(t, e, conv) != 4 || !ai.throttled || level(conv) != 2 || lastOut(t, e, conv).SenderType != "ai" {
		t.Fatalf("after 5 min: out=%d throttled=%v level=%d", outboundCount(t, e, conv), ai.throttled, level(conv))
	}
	// 再過 6 分：未滿 10 分 → 不回；再過 5 分（共 11 分）→ 回，升 3 級（20 分）
	ago(t, e, conv, "direction = 'out'", 6*time.Minute)
	say("嗨")
	if outboundCount(t, e, conv) != 4 {
		t.Fatal("interval should have doubled to 10 min")
	}
	ago(t, e, conv, "direction = 'out'", 5*time.Minute)
	say("嗨")
	if outboundCount(t, e, conv) != 5 || level(conv) != 3 {
		t.Fatalf("after 11 min: out=%d level=%d", outboundCount(t, e, conv), level(conv))
	}
	if contactRisk(t, e, conv).risk != inbox.RiskWatch {
		t.Fatal("throttled contact must never be blocked by rate")
	}

	// 安靜超過目前間隔（20 分）→ 歸零、正常回；問題顧客標記保留
	ago(t, e, conv, "true", 30*time.Minute)
	say("嗨")
	if level(conv) != 0 || outboundCount(t, e, conv) != 6 {
		t.Fatalf("quiet reset: level=%d out=%d", level(conv), outboundCount(t, e, conv))
	}
	ai.d = AIDecision{Action: "reply", Reply: "好的"}
	say("請問有開嗎")
	if ai.throttled || outboundCount(t, e, conv) != 7 {
		t.Fatalf("after reset AI should not be throttled: throttled=%v", ai.throttled)
	}
	if r := contactRisk(t, e, conv); r.risk != inbox.RiskWatch || r.source != inbox.RiskSourceRate {
		t.Fatalf("watch kept after reset: %+v", r)
	}

	// 再次大量 → 降頻；該轉真人時照樣轉、照樣通知
	ago(t, e, conv, "true", 30*time.Minute)
	for i := 0; i < 4; i++ {
		say("嗨")
	}
	if level(conv) != 1 {
		t.Fatalf("throttle again: level=%d", level(conv))
	}
	ago(t, e, conv, "direction = 'out'", 6*time.Minute)
	ai.d = AIDecision{Action: "handoff", Reason: "要店員確認"}
	drain(events)
	say("我想訂包場")
	c, _ := e.Inbox.GetConversation(ctx, conv)
	if !hasEvent(eventsOf(events), "handoff.requested") || c.Handler != "human" || lastOut(t, e, conv).Text != "請稍等店員" {
		t.Fatalf("handoff while throttled: handler=%s", c.Handler)
	}
}

// AI 判斷騷擾 → 列入黑名單（來源 ai），不回覆、不轉真人；設定關閉時忽略 abusive。
func TestAIAbusiveBlocks(t *testing.T) {
	e, ai, acc := setup(t)
	ctx := context.Background()
	st, _ := e.Store.GetSettings(ctx, acc)
	events, unsub := e.Inbox.Hub.Subscribe()
	defer unsub()

	ai.d = AIDecision{Action: "handoff", Intent: "other", Reason: "內容不當", Abusive: true, AbuseReason: "辱罵店員"}
	conv, m := customerSays(t, e, "u1", "m1", "（髒話）")
	drain(events)
	if err := e.handle(ctx, conv, m.ID, st); err != nil {
		t.Fatal(err)
	}
	r := contactRisk(t, e, conv)
	if r.risk != inbox.RiskBlocked || r.source != inbox.RiskSourceAI || r.reason != "辱罵店員" {
		t.Fatalf("ai block: %+v", r)
	}
	c, _ := e.Inbox.GetConversation(ctx, conv)
	if outboundCount(t, e, conv) != 0 || c.Handler != "bot" {
		t.Fatalf("abusive message must not be answered or handed off (handler=%s)", c.Handler)
	}
	if hasEvent(eventsOf(events), "handoff.requested") {
		t.Fatal("ai block must not publish handoff.requested")
	}
	if got := reviewsOf(t, e); len(got) != 0 {
		t.Fatalf("abusive should not record ai_unsure: %+v", got)
	}

	// 已封鎖的不再呼叫 AI
	before := ai.calls
	conv, m = customerSays(t, e, "u1", "m2", "（又是髒話）")
	if err := e.handle(ctx, conv, m.ID, st); err != nil || ai.calls != before {
		t.Fatalf("blocked: err=%v calls %d→%d", err, before, ai.calls)
	}

	// 關閉 AI 判斷騷擾 → abusive 不算數，照常回
	st.AIModeration = false
	ai.d = AIDecision{Action: "reply", Reply: "您好", Abusive: true, AbuseReason: "x"}
	conv, m = customerSays(t, e, "u2", "m3", "哈囉")
	if err := e.handle(ctx, conv, m.ID, st); err != nil {
		t.Fatal(err)
	}
	if r := contactRisk(t, e, conv); r.risk != "" || lastOut(t, e, conv).Text != "您好" {
		t.Fatalf("moderation off: %+v", r)
	}
}

// AI 轉真人 → 記 ai_unsure（掛顧客那則、note＝AI 理由）；模型拒答也記 ai_unsure，但不封鎖。
func TestAIHandoffAndRefusalRecordUnsure(t *testing.T) {
	e, ai, acc := setup(t)
	ctx := context.Background()
	st, _ := e.Store.GetSettings(ctx, acc)

	ai.d = AIDecision{Action: "handoff", Intent: "reservation", Reason: "要確認空位"}
	conv, m := customerSays(t, e, "u1", "m1", "週六還有位子嗎")
	if err := e.handle(ctx, conv, m.ID, st); err != nil {
		t.Fatal(err)
	}
	got := reviewsOf(t, e)
	if len(got) != 1 || got[0].Kind != ReviewAIUnsure || got[0].MessageID != m.ID || got[0].Note != "要確認空位" ||
		got[0].Question != "週六還有位子嗎" || got[0].ConversationID != conv || got[0].Intent != "reservation" || got[0].ResolvedAt != nil {
		t.Fatalf("ai_unsure: %+v", got)
	}

	events, unsub := e.Inbox.Hub.Subscribe()
	defer unsub()
	ai.err = ErrRefused
	conv, m = customerSays(t, e, "u2", "m2", "奇怪的問題")
	drain(events)
	if err := e.handle(ctx, conv, m.ID, st); err != nil {
		t.Fatal(err)
	}
	got = reviewsOf(t, e)
	if len(got) != 2 || got[0].Kind != ReviewAIUnsure || got[0].MessageID != m.ID || !strings.Contains(got[0].Note, "拒答") {
		t.Fatalf("refusal ai_unsure: %+v", got)
	}
	if r := contactRisk(t, e, conv); r.risk != "" {
		t.Fatalf("refusal must not block: %+v", r)
	}
	if !hasEvent(eventsOf(events), "handoff.requested") {
		t.Fatal("refusal should still hand off")
	}
	// 同一則重跑不重複記
	if err := e.Store.AddUnsure(ctx, m.ID, acc, "again"); err != nil || len(reviewsOf(t, e)) != 2 {
		t.Fatalf("duplicate unsure: %v", err)
	}
}

// 沙盒（Evaluate）顯示騷擾判斷，但不寫 DB。
func TestEvaluateShowsAbusiveWithoutWriting(t *testing.T) {
	e, ai, acc := setup(t)
	ctx := context.Background()
	st, _ := e.Store.GetSettings(ctx, acc)
	ai.d = AIDecision{Action: "reply", Reply: "", Abusive: true, AbuseReason: "索取店員電話"}
	out, err := e.Evaluate(ctx, acc, st, []inbox.Message{{Direction: "in", Kind: "text", Text: "給我店員的電話"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != "block" || out.Detail != "索取店員電話" || out.AI == nil || !out.AI.Abusive {
		t.Fatalf("sandbox outcome: %+v", out)
	}
	var n int
	_ = e.Inbox.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM contacts WHERE risk <> '') + (SELECT count(*) FROM message_reviews)`).Scan(&n)
	if n != 0 {
		t.Fatalf("sandbox wrote %d rows", n)
	}
}

func TestDecisionSchemaModeration(t *testing.T) {
	on, off := decisionSchema(true), decisionSchema(false)
	if req := on["required"].([]string); len(req) != 6 || req[4] != "abusive" {
		t.Fatalf("schema with moderation: %v", req)
	}
	if _, ok := off["properties"].(map[string]any)["abusive"]; ok {
		t.Fatal("schema without moderation should not ask for abusive")
	}
	if p := systemPrompt(Settings{AIModeration: true}, nil); !strings.Contains(p, "abusive") || !strings.Contains(p, "客訴") {
		t.Fatal("prompt with moderation should explain abusive")
	}
	if p := systemPrompt(Settings{}, nil); strings.Contains(p, "abusive") || strings.Contains(p, "降頻模式") {
		t.Fatal("prompt without moderation/throttle should not mention them")
	}
	if p := systemPrompt(Settings{Throttled: true}, nil); !strings.Contains(p, "降頻模式") || !strings.Contains(p, "不要有敵意") {
		t.Fatal("throttled prompt should ask for a short, gentle reply")
	}
}
