package inbox

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// SenderSystem 是系統事件訊息（例：真人閒置交回 AI）：direction='out'、kind='system'，不送 IG，前端顯示成置中灰字。
// 分析、摘要、蒸餾、回應時間、AI 對話紀錄一律排除（見 migration 00019）。
const SenderSystem = "system"

// IsSystem 回這則是不是系統事件訊息。
func (m Message) IsSystem() bool { return m.SenderType == SenderSystem }

// AddSystemMessage 在對話裡寫一筆系統事件（不送 IG、不動對話的最後訊息／未讀），並推 message.created。
func (s *Service) AddSystemMessage(ctx context.Context, convID int64, text string) (Message, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Message{}, ErrEmptyText
	}
	var id int64
	err := s.DB.QueryRow(ctx, `
INSERT INTO messages (conversation_id, direction, sender_type, kind, text, created_at)
VALUES ($1, 'out', 'system', 'system', $2, now()) RETURNING id`, convID, text).Scan(&id)
	if err != nil {
		return Message{}, err
	}
	m, err := s.GetMessage(ctx, id)
	if err != nil {
		return Message{}, err
	}
	s.publishMessage(m)
	return m, nil
}

// UnansweredInbound 回「需要真人回覆」的那則顧客訊息：最後一則未刪除的顧客訊息，且它之後沒有真人（sender_type='human'）送出的訊息。
// ok=false＝目前沒有需要回覆的顧客訊息（沒有顧客訊息，或真人已回）。
func (s *Service) UnansweredInbound(ctx context.Context, convID int64) (id int64, at time.Time, ok bool, err error) {
	err = s.DB.QueryRow(ctx, `
SELECT li.id, li.created_at FROM (
  SELECT m.id, m.created_at FROM messages m
  WHERE m.conversation_id = $1 AND m.direction = 'in' AND m.deleted_at IS NULL
  ORDER BY m.created_at DESC, m.id DESC LIMIT 1
) li
WHERE NOT EXISTS (SELECT 1 FROM messages h WHERE h.conversation_id = $1 AND h.direction = 'out' AND h.sender_type = 'human'
                  AND (h.created_at, h.id) > (li.created_at, li.id))`, convID).Scan(&id, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, time.Time{}, false, nil
	}
	if err != nil {
		return 0, time.Time{}, false, err
	}
	return id, at, true, nil
}

// IdleHandback 真人閒置交回 AI：handler human → bot，並記下「已因閒置交回處理過的顧客訊息」msgID（同一則只交回一次）。
// 條件（同一個 UPDATE 內判斷）：仍是 human、msgID 仍是最後一則未刪除的顧客訊息、之後沒有真人回覆、這則還沒交回過；
// 不符回 false、不改資料；成功推 conversation.updated。
func (s *Service) IdleHandback(ctx context.Context, convID, msgID int64) (bool, error) {
	tag, err := s.DB.Exec(ctx, `
UPDATE conversations c SET handler = 'bot', idle_handback_msg_id = $2
WHERE c.id = $1 AND c.handler = 'human' AND c.idle_handback_msg_id IS DISTINCT FROM $2
  AND $2 = (SELECT m.id FROM messages m WHERE m.conversation_id = $1 AND m.direction = 'in' AND m.deleted_at IS NULL
            ORDER BY m.created_at DESC, m.id DESC LIMIT 1)
  AND NOT EXISTS (SELECT 1 FROM messages h JOIN messages li ON li.id = $2
                  WHERE h.conversation_id = $1 AND h.direction = 'out' AND h.sender_type = 'human'
                    AND (h.created_at, h.id) > (li.created_at, li.id))`, convID, msgID)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if c, err := s.GetConversation(ctx, convID); err == nil {
		s.publishConversation(c)
	}
	return true, nil
}
