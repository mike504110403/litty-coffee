package automation

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/mike504110403/litty-coffee/backend/internal/inbox"
)

// 真人閒置自動交回 AI：handler=human、帳號自動回覆開著（且 AI 開著）、顧客不是黑名單、仍在 IG 24 小時回覆窗口內，
// 而且「最後一則未回覆的顧客訊息」（之後沒有真人回覆）距今 ≥ automation_settings.human_idle_minutes 分鐘
// → handler 改回 bot、寫一筆系統事件、讓 AI 走既有引擎（規則 → AI）處理那則等著的訊息，提示 AI 先致歉「久等了」。
// AI 又轉真人時不再送轉真人訊息；同一則顧客訊息只交回一次（conversations.idle_handback_msg_id，重啟後也成立）。
// 背景動作不記操作紀錄。

// IdleScanInterval 是閒置掃描的間隔。
const IdleScanInterval = time.Minute

type idleCandidate struct {
	ConvID    int64
	AccountID int64
	MsgID     int64
	MsgAt     time.Time
	Minutes   int
}

// idleCandidates 找出此刻該交回 AI 的對話。
func (s *Store) idleCandidates(ctx context.Context, now time.Time) ([]idleCandidate, error) {
	// 帳號還沒有自己的設定列（從沒呼叫過 EnsureAccount）時用範本的值（GetSettings 屆時也會從範本複製）
	rows, err := s.DB.Query(ctx, `
WITH cfg AS (
  SELECT a.id AS account_id,
         COALESCE(st.auto_reply_enabled, tpl.auto_reply_enabled) AS auto_reply_enabled,
         COALESCE(st.ai_enabled, tpl.ai_enabled) AS ai_enabled,
         COALESCE(st.human_idle_minutes, tpl.human_idle_minutes) AS human_idle_minutes
  FROM ig_accounts a
  LEFT JOIN automation_settings st ON st.ig_account_id = a.id
  LEFT JOIN automation_settings tpl ON tpl.ig_account_id IS NULL
)
SELECT c.id, ct.ig_account_id, li.id, li.created_at, cfg.human_idle_minutes
FROM conversations c
JOIN contacts ct ON ct.id = c.contact_id
JOIN cfg ON cfg.account_id = ct.ig_account_id
CROSS JOIN LATERAL (
  SELECT m.id, m.created_at FROM messages m
  WHERE m.conversation_id = c.id AND m.direction = 'in' AND m.deleted_at IS NULL
  ORDER BY m.created_at DESC, m.id DESC LIMIT 1
) li
WHERE c.handler = 'human' AND cfg.auto_reply_enabled AND cfg.ai_enabled AND cfg.human_idle_minutes > 0
  AND ct.risk <> 'blocked'
  AND c.last_inbound_at > $1::timestamptz - interval '24 hours'
  AND li.created_at <= $1::timestamptz - make_interval(mins => cfg.human_idle_minutes)
  AND c.idle_handback_msg_id IS DISTINCT FROM li.id
  AND NOT EXISTS (SELECT 1 FROM messages h WHERE h.conversation_id = c.id AND h.direction = 'out' AND h.sender_type = 'human'
                  AND (h.created_at, h.id) > (li.created_at, li.id))
ORDER BY li.created_at`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []idleCandidate
	for rows.Next() {
		var c idleCandidate
		if err := rows.Scan(&c.ConvID, &c.AccountID, &c.MsgID, &c.MsgAt, &c.Minutes); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RunIdleHandback 每分鐘掃一次，直到 ctx 結束。
func (e *Engine) RunIdleHandback(ctx context.Context) {
	tick := time.NewTicker(IdleScanInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if _, err := e.IdleHandbackOnce(ctx, time.Now()); err != nil {
				log.Printf("automation: idle handback: %v", err)
			}
		}
	}
}

// IdleHandbackOnce 掃一次並處理，回交回的對話數。沒有可用的 AI（沒 key）就不交回。
func (e *Engine) IdleHandbackOnce(ctx context.Context, now time.Time) (int, error) {
	if !e.AIEnabled() {
		return 0, nil
	}
	cands, err := e.Store.idleCandidates(ctx, now)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range cands {
		ok, err := e.handBack(ctx, c, now)
		if err != nil {
			log.Printf("automation: idle handback conv %d: %v", c.ConvID, err)
		}
		if ok {
			n++
		}
	}
	return n, nil
}

func (e *Engine) handBack(ctx context.Context, c idleCandidate, now time.Time) (bool, error) {
	st, err := e.Store.GetSettings(ctx, c.AccountID)
	if err != nil {
		return false, err
	}
	if !st.AutoReplyEnabled || !st.AIEnabled || st.HumanIdleMinutes <= 0 {
		return false, nil
	}
	mu := e.lock(c.ConvID)
	defer mu.Unlock()
	conv, err := e.Inbox.GetConversation(ctx, c.ConvID)
	if err != nil {
		return false, err
	}
	if conv.Contact.Risk == inbox.RiskBlocked {
		return false, nil
	}
	// 條件式更新：仍是 human、這則仍是最後一則且真人沒回、這則還沒交回過 → 改 bot 並記下這則
	ok, err := e.Inbox.IdleHandback(ctx, c.ConvID, c.MsgID)
	if err != nil || !ok {
		return false, err
	}
	if _, err := e.Inbox.AddSystemMessage(ctx, c.ConvID, fmt.Sprintf("真人 %d 分鐘未回覆，已交回 AI", st.HumanIdleMinutes)); err != nil {
		log.Printf("automation: idle handback conv %d: system message: %v", c.ConvID, err)
	}
	st.WaitedMinutes = max(int(now.Sub(c.MsgAt)/time.Minute), st.HumanIdleMinutes)
	human, err := e.process(ctx, c.ConvID, c.MsgID, st, handleOpts{idle: true})
	if err != nil {
		// 送出失敗等：別讓對話停在 bot 卻沒人回
		if herr := e.Inbox.SetHandler(ctx, c.ConvID, "human"); herr != nil {
			log.Printf("automation: idle handback conv %d: revert handler: %v", c.ConvID, herr)
		}
		human = true
	}
	if human {
		e.Suggest.Schedule(c.ConvID, c.MsgID)
	}
	return true, err
}
