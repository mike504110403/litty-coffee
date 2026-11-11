package automation

import (
	"context"
	"encoding/json"
	"errors"
	"log"
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
	Kind   string      `json:"kind"` // rule | ai | handoff | none
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
func (e *Engine) OnInbound(convID int64, msg inbox.Message) {
	go func() {
		st, err := e.Store.GetSettings(e.Ctx)
		if err != nil {
			log.Printf("automation: settings: %v", err)
			return
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
	conv, err := e.Inbox.GetConversation(ctx, convID)
	if err != nil {
		return err
	}
	if conv.Handler != "bot" {
		return nil // 真人接手中
	}
	history, err := e.Inbox.ListMessages(ctx, convID, 0, historyTurns)
	if err != nil || len(history) == 0 {
		return err
	}
	last := history[len(history)-1]
	if last.ID != msgID {
		return nil // 有更新的訊息（或店家已回覆），交給後面那次處理
	}

	out, err := e.Evaluate(ctx, st, history)
	if err != nil {
		return err
	}
	if out.AI != nil {
		e.tagIntent(ctx, msgID, out.AI.Intent)
	}
	switch out.Kind {
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
			return e.Inbox.SetHandler(ctx, convID, "human")
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
		return e.Inbox.SetHandler(ctx, convID, "human")
	}
	return nil
}

// Evaluate 決定要怎麼回：先比關鍵字規則，沒命中再問 AI。不送出、不改資料。
func (e *Engine) Evaluate(ctx context.Context, st Settings, history []inbox.Message) (Outcome, error) {
	if len(history) == 0 {
		return Outcome{Kind: "none"}, nil
	}
	last := history[len(history)-1]
	if last.Direction != "in" {
		return Outcome{Kind: "none", Detail: "最後一則不是顧客訊息"}, nil
	}

	rules, err := e.Store.ListRules(ctx, true)
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

	kb, err := e.Store.ListKnowledge(ctx)
	if err != nil {
		return Outcome{}, err
	}
	turns := make([]Turn, 0, len(history))
	for _, m := range history {
		turns = append(turns, Turn{FromCustomer: m.Direction == "in", Text: m.Text})
	}
	d, err := e.AI.Decide(ctx, st, kb, turns)
	if errors.Is(err, ErrRefused) {
		return Outcome{Kind: "handoff", Reply: st.HandoffMessage, Detail: "模型拒答，轉真人"}, nil
	}
	if err != nil {
		return Outcome{}, err
	}
	if d.Action == "handoff" {
		return Outcome{Kind: "handoff", AI: &d, Reply: st.HandoffMessage}, nil
	}
	return Outcome{Kind: "ai", AI: &d, Reply: d.Reply}, nil
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
