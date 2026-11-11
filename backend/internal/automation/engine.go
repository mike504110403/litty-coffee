package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/mike504110403/litty-coffee/backend/internal/inbox"
)

// Decider 抽象 AI，測試時可替換。
type Decider interface {
	Decide(ctx context.Context, st Settings, kb []Knowledge, turns []Turn) (AIDecision, error)
}

// Outcome 描述自動回覆對一則訊息的處理結果（試答沙盒也用它）。
type Outcome struct {
	Kind   string      `json:"kind"` // rule | ai | handoff | block（AI 判斷騷擾）| none
	Rule   *Rule       `json:"rule,omitempty"`
	AI     *AIDecision `json:"ai,omitempty"`
	Reply  string      `json:"reply"`
	Detail string      `json:"detail,omitempty"`
}

type Engine struct {
	Store *Store
	Inbox *inbox.Service
	AI    Decider // nil 或 LiveAI 未啟用＝沒有 Claude API Key，只跑規則
	Ctx   context.Context
	// Calls 記最近一次真的打出去的 AI 呼叫結果與耗時（系統狀態用）；nil＝不記
	Calls *CallStats

	locks sync.Map // convID -> *sync.Mutex
}

// AIEnabled 回 AI 是否可用：沒掛 Decider，或掛的是尚未設 key 的 LiveAI 都算未啟用。
func (e *Engine) AIEnabled() bool {
	if e.AI == nil {
		return false
	}
	if l, ok := e.AI.(interface{ Enabled() bool }); ok {
		return l.Enabled()
	}
	return true
}

const historyTurns = 20

// OnInbound 掛在 inbox.Service.OnInbound：等合併時間後，只處理該對話最新一則顧客訊息。
// 用的是「這段對話所屬帳號」的設定、知識庫與規則。
func (e *Engine) OnInbound(convID int64, msg inbox.Message) {
	go func() {
		st, err := e.Store.GetSettings(e.Ctx, msg.IGAccountID)
		if err != nil {
			log.Printf("automation: settings: %v", err)
			return
		}
		if !st.AutoReplyEnabled {
			return // 自動回覆總開關關閉：只收不回，連合併等待都不必
		}
		select {
		case <-e.Ctx.Done():
			return
		case <-time.After(time.Duration(st.DebounceSecs) * time.Second):
		}
		mu, _ := e.locks.LoadOrStore(convID, &sync.Mutex{})
		mu.(*sync.Mutex).Lock()
		defer mu.(*sync.Mutex).Unlock()
		if err := e.handle(e.Ctx, convID, msg.ID, st); err != nil {
			log.Printf("automation: conv %d: %v", convID, err)
		}
	}()
}

func (e *Engine) handle(ctx context.Context, convID, msgID int64, st Settings) error {
	if !st.AutoReplyEnabled {
		return nil // 總開關關閉：不跑規則、AI、降頻，也不發轉真人通知
	}
	conv, err := e.Inbox.GetConversation(ctx, convID)
	if err != nil {
		return err
	}
	if conv.Contact.Risk == inbox.RiskBlocked {
		return nil // 黑名單：訊息照收，但不跑規則與 AI、不發轉真人通知
	}
	if conv.Handler != "bot" {
		return nil // 真人接手中
	}
	// 降頻（大量訊息）：比規則還早判斷；每則顧客訊息都檢查（含稍後會因有更新訊息而略過的）
	level, err := e.updateThrottle(ctx, conv, msgID, st)
	if err != nil {
		return err
	}
	history, err := e.Inbox.ListMessages(ctx, convID, 0, historyTurns)
	if err != nil || len(history) == 0 {
		return err
	}
	last := history[len(history)-1]
	if last.ID != msgID {
		return nil // 有更新的訊息（或店家已回覆），交給後面那次處理
	}
	if level > 0 {
		// 降頻中：距上次自動回覆未滿目前間隔就只收不回
		lastReply, err := e.Inbox.LastAutoReplyAt(ctx, convID)
		if err != nil {
			return err
		}
		if lastReply != nil && time.Since(*lastReply) < ThrottleInterval(level) {
			return nil
		}
		st.Throttled = true // 請 AI 回得簡短、委婉
	}

	out, err := e.Evaluate(ctx, conv.IGAccountID, st, history)
	if err != nil {
		return err
	}
	if level > 0 && out.Kind != "block" && out.Reply != "" {
		// 降頻中回了一次 → 間隔加倍（送出失敗也升級，避免一直重試）
		if err := e.Inbox.SetThrottle(ctx, conv.Contact.ID, min(level+1, maxThrottleLevel)); err != nil {
			log.Printf("automation: throttle level: %v", err)
		}
	}
	if out.AI != nil {
		e.tagIntent(ctx, msgID, out.AI.Intent)
	}
	switch out.Kind {
	case "block":
		_, err := e.Inbox.AutoBlock(ctx, conv.Contact.ID, out.Detail)
		return err
	case "rule":
		e.Store.RecordHit(ctx, out.Rule.ID)
		if out.Reply != "" {
			m, err := e.Inbox.Send(ctx, convID, out.Reply, "bot")
			if err != nil {
				return err
			}
			e.tagMeta(ctx, m.ID, map[string]any{"ruleId": out.Rule.ID, "ruleName": out.Rule.Name})
		}
		if out.Rule.Handoff {
			return e.Inbox.Handoff(ctx, convID, "命中關鍵字規則「"+out.Rule.Name+"」")
		}
	case "ai":
		m, err := e.Inbox.Send(ctx, convID, out.Reply, "ai")
		if err != nil {
			return err
		}
		e.tagMeta(ctx, m.ID, map[string]any{"intent": out.AI.Intent, "reason": out.AI.Reason})
	case "handoff":
		if out.Reply != "" {
			if _, err := e.Inbox.Send(ctx, convID, out.Reply, "bot"); err != nil && !errors.Is(err, inbox.ErrWindowClosed) {
				return err
			}
		}
		reason := out.Detail
		if out.AI != nil && out.AI.Reason != "" {
			reason = out.AI.Reason
		}
		if reason == "" {
			reason = "AI 沒把握回答"
		}
		// AI 轉真人（含模型拒答）＝AI 沒把握：記一筆待改進，掛在顧客那則訊息上
		if err := e.Store.AddUnsure(ctx, msgID, conv.IGAccountID, reason); err != nil {
			log.Printf("automation: record ai_unsure: %v", err)
		}
		return e.Inbox.Handoff(ctx, convID, reason)
	}
	return nil
}

// Evaluate 決定要怎麼回：先比該帳號的關鍵字規則，沒命中再問 AI（用該帳號的知識庫）。不送出、不改資料。
func (e *Engine) Evaluate(ctx context.Context, accountID int64, st Settings, history []inbox.Message) (Outcome, error) {
	if len(history) == 0 {
		return Outcome{Kind: "none"}, nil
	}
	last := history[len(history)-1]
	if last.Direction != "in" {
		return Outcome{Kind: "none", Detail: "最後一則不是顧客訊息"}, nil
	}

	rules, err := e.Store.ListRules(ctx, accountID, true)
	if err != nil {
		return Outcome{}, err
	}
	if r, ok := MatchRule(rules, last.Text); ok {
		return Outcome{Kind: "rule", Rule: &r, Reply: r.Reply}, nil
	}

	if last.Kind != "text" && last.Text == "" {
		return Outcome{Kind: "none", Detail: "非文字訊息，留給真人"}, nil
	}
	if !st.AIEnabled {
		return Outcome{Kind: "none", Detail: "AI 回覆已關閉"}, nil
	}
	if !e.AIEnabled() {
		return Outcome{Kind: "none", Detail: "尚未設定 Claude API Key"}, nil
	}

	kb, err := e.Store.ListKnowledge(ctx, accountID)
	if err != nil {
		return Outcome{}, err
	}
	turns := make([]Turn, 0, len(history))
	for _, m := range history {
		turns = append(turns, Turn{FromCustomer: m.Direction == "in", Text: m.Text})
	}
	start := time.Now()
	d, err := e.AI.Decide(ctx, st, kb, turns)
	if !errors.Is(err, ErrAIDisabled) {
		e.Calls.Record("reply", start, err)
	}
	if errors.Is(err, ErrRefused) {
		return Outcome{Kind: "handoff", Reply: st.HandoffMessage, Detail: "模型拒答，轉真人"}, nil
	}
	if err != nil {
		return Outcome{}, err
	}
	if st.AIModeration && d.Abusive {
		reason := strings.TrimSpace(d.AbuseReason)
		if reason == "" {
			reason = "AI 判斷為騷擾或惡意訊息"
		}
		return Outcome{Kind: "block", AI: &d, Detail: reason}, nil
	}
	if d.Action == "handoff" {
		return Outcome{Kind: "handoff", AI: &d, Reply: st.HandoffMessage}, nil
	}
	return Outcome{Kind: "ai", AI: &d, Reply: d.Reply}, nil
}

// 降頻：大量訊息不封鎖，改成拉長自動回覆的間隔。等級 N≥1 時每 5×2^(N-1) 分鐘最多回一次，上限 6 小時。
const (
	throttleBase     = 5 * time.Minute
	throttleCap      = 6 * time.Hour
	maxThrottleLevel = 8 // 5×2^7 分鐘已超過上限
)

// ThrottleInterval 回降頻等級對應的最短回覆間隔；0 級回 0。
func ThrottleInterval(level int) time.Duration {
	if level <= 0 {
		return 0
	}
	d := throttleBase << (level - 1)
	if d > throttleCap || d <= 0 {
		return throttleCap
	}
	return d
}

// updateThrottle 依這則顧客訊息更新降頻狀態，回傳目前等級：
//   - 降頻中且顧客安靜超過目前間隔（與前一則顧客訊息相隔更久）→ 歸零（問題顧客標記保留，留作紀錄）
//   - 未降頻且 spamWindowSecs 內超過 spamMaxMsgs 則 → 進入降頻（1 級）並自動標成問題顧客（來源 rate）
func (e *Engine) updateThrottle(ctx context.Context, conv inbox.Conversation, msgID int64, st Settings) (int, error) {
	level := conv.Contact.ThrottleLevel
	if level > 0 {
		gap, ok, err := e.Inbox.InboundGap(ctx, msgID)
		if err != nil {
			return level, err
		}
		if ok && gap > ThrottleInterval(level) {
			if err := e.Inbox.SetThrottle(ctx, conv.Contact.ID, 0); err != nil {
				return level, err
			}
			level = 0
		}
	}
	if level > 0 || st.SpamMaxMsgs <= 0 || st.SpamWindowSecs <= 0 {
		return level, nil
	}
	n, err := e.Inbox.RecentInbound(ctx, conv.ID, time.Duration(st.SpamWindowSecs)*time.Second)
	if err != nil || n <= st.SpamMaxMsgs {
		return level, err
	}
	if err := e.Inbox.SetThrottle(ctx, conv.Contact.ID, 1); err != nil {
		return level, err
	}
	reason := fmt.Sprintf("%s內傳了 %d 則，已降低回覆頻率", windowLabel(st.SpamWindowSecs), n)
	if _, err := e.Inbox.AutoWatch(ctx, conv.Contact.ID, reason); err != nil {
		return 1, err
	}
	return 1, nil
}

func windowLabel(secs int) string {
	if secs%60 == 0 {
		return fmt.Sprintf("%d 分鐘", secs/60)
	}
	return fmt.Sprintf("%d 秒", secs)
}

func (e *Engine) tagIntent(ctx context.Context, msgID int64, intent string) {
	if intent != "" {
		e.tagMeta(ctx, msgID, map[string]any{"intent": intent})
	}
}

func (e *Engine) tagMeta(ctx context.Context, msgID int64, kv map[string]any) {
	b, _ := json.Marshal(kv)
	if _, err := e.Inbox.DB.Exec(ctx, `UPDATE messages SET meta = meta || $2::jsonb WHERE id = $1`, msgID, b); err != nil {
		log.Printf("automation: tag meta: %v", err)
	}
}
