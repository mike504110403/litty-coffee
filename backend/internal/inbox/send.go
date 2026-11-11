package inbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Send 從後台（human）或自動回覆（bot / ai）送一則文字訊息給顧客。
// 真人送出會把對話切成 handler=human（bot 暫停），業界慣例。
func (s *Service) Send(ctx context.Context, convID int64, text, senderType string) (Message, error) {
	return s.send(ctx, convID, text, senderType, 0)
}

// SendAsAdmin 是後台管理員的真人回覆，記下送出者（messages.sent_by_admin_id）。
func (s *Service) SendAsAdmin(ctx context.Context, convID int64, text string, adminID int64) (Message, error) {
	return s.send(ctx, convID, text, "human", adminID)
}

func (s *Service) send(ctx context.Context, convID int64, text, senderType string, adminID int64) (Message, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Message{}, ErrEmptyText
	}
	var accountIGID, token, igsid string
	var lastInbound *time.Time
	err := s.DB.QueryRow(ctx, `
SELECT a.ig_user_id, a.access_token, ct.igsid, c.last_inbound_at
FROM conversations c JOIN contacts ct ON ct.id = c.contact_id JOIN ig_accounts a ON a.id = ct.ig_account_id
WHERE c.id = $1`, convID).Scan(&accountIGID, &token, &igsid, &lastInbound)
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, err
	}

	humanAgent, err := windowCheck(lastInbound, senderType, time.Now())
	if err != nil {
		return Message{}, err
	}

	if token == "" {
		token = s.DefaultToken
	}
	mid, err := s.IG.SendText(ctx, accountIGID, token, igsid, text, humanAgent)
	if err != nil {
		return Message{}, fmt.Errorf("IG 送出失敗：%w", err)
	}

	now := time.Now()
	m := Message{ConversationID: convID, Direction: "out", SenderType: senderType, Kind: "text", Text: text}
	var sentBy *int64
	if adminID > 0 {
		sentBy = &adminID
	}
	var sentByName *string
	// echo 可能比這裡先到並以 human 落庫；以我方的 sender_type／送出者為準覆寫
	if err := s.DB.QueryRow(ctx, `
INSERT INTO messages (conversation_id, ig_mid, direction, sender_type, kind, text, created_at, sent_by_admin_id)
VALUES ($1, $2, 'out', $3, 'text', $4, $5, $6)
ON CONFLICT (ig_mid) DO UPDATE SET sender_type = EXCLUDED.sender_type, sent_by_admin_id = EXCLUDED.sent_by_admin_id
RETURNING id, created_at,
  (SELECT COALESCE(NULLIF(display_name, ''), username) FROM admin_users WHERE id = $6)`,
		convID, mid, senderType, text, now, sentBy).Scan(&m.ID, &m.CreatedAt, &sentByName); err != nil {
		return Message{}, err
	}
	if sentBy != nil && sentByName != nil {
		m.SentBy = &Sender{ID: adminID, DisplayName: *sentByName}
	}

	handlerSQL := ""
	if senderType == "human" {
		handlerSQL = ", handler = 'human'"
	}
	if _, err := s.DB.Exec(ctx, `
UPDATE conversations SET last_message_at = $2, last_message_text = $3, unread_count = 0`+handlerSQL+`
WHERE id = $1`, convID, now, text); err != nil {
		return Message{}, err
	}

	s.publishMessage(m)
	if c, err := s.GetConversation(ctx, convID); err == nil {
		s.publishConversation(c)
	}
	return m, nil
}

// windowCheck 依 IG 規則判斷能否送出：24h 內一般送出；24h～7d 只有真人能用 HUMAN_AGENT tag。
func windowCheck(lastInbound *time.Time, senderType string, now time.Time) (humanAgent bool, err error) {
	if lastInbound == nil {
		return false, ErrWindowClosed
	}
	age := now.Sub(*lastInbound)
	switch {
	case age <= ReplyWindow:
		return false, nil
	case senderType == "human" && age <= HumanAgentWindow:
		return true, nil
	default:
		return false, ErrWindowClosed
	}
}
