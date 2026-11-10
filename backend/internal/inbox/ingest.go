package inbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mike504110403/litty-coffee/backend/internal/ig"
)

// EnqueueWebhook 把原始 payload 落庫並喚醒 worker；webhook handler 與模擬器共用。
func (s *Service) EnqueueWebhook(ctx context.Context, payload []byte) error {
	if _, err := s.DB.Exec(ctx, `INSERT INTO webhook_events (payload) VALUES ($1)`, payload); err != nil {
		return err
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

type ingested struct {
	convID    int64
	contactID int64
	needsProf bool
	igsid     string
	token     string
	msg       Message
	isInbound bool
}

// processPayload 在呼叫端的交易內把一個 webhook payload 寫進領域資料表。
func processPayload(ctx context.Context, tx pgx.Tx, raw []byte) ([]ingested, error) {
	var p ig.Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	var out []ingested
	for _, e := range p.Entry {
		for _, m := range e.Messaging {
			if m.Message == nil || m.Message.MID == "" || m.Message.IsDeleted {
				continue // 已讀、reaction、刪除等事件之後再處理
			}
			r, ok, err := ingestMessage(ctx, tx, e.ID, m)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

func ingestMessage(ctx context.Context, tx pgx.Tx, accountIGID string, m ig.Messaging) (ingested, bool, error) {
	var r ingested
	inbound := !m.Message.IsEcho
	r.isInbound = inbound
	r.igsid = m.Sender.ID
	if !inbound {
		r.igsid = m.Recipient.ID
	}
	ts := time.UnixMilli(m.Timestamp)
	if m.Timestamp == 0 {
		ts = time.Now()
	}

	var accountID int64
	if err := tx.QueryRow(ctx, `
INSERT INTO ig_accounts (ig_user_id) VALUES ($1)
ON CONFLICT (ig_user_id) DO UPDATE SET ig_user_id = EXCLUDED.ig_user_id
RETURNING id, access_token`, accountIGID).Scan(&accountID, &r.token); err != nil {
		return r, false, err
	}

	var username string
	if err := tx.QueryRow(ctx, `
INSERT INTO contacts (ig_account_id, igsid, first_seen_at, last_seen_at) VALUES ($1, $2, $3, $3)
ON CONFLICT (ig_account_id, igsid) DO UPDATE
  SET last_seen_at = CASE WHEN $4 THEN GREATEST(contacts.last_seen_at, EXCLUDED.last_seen_at) ELSE contacts.last_seen_at END
RETURNING id, username`, accountID, r.igsid, ts, inbound).Scan(&r.contactID, &username); err != nil {
		return r, false, err
	}
	r.needsProf = username == ""

	if err := tx.QueryRow(ctx, `
INSERT INTO conversations (contact_id) VALUES ($1)
ON CONFLICT (contact_id) DO UPDATE SET contact_id = EXCLUDED.contact_id
RETURNING id`, r.contactID).Scan(&r.convID); err != nil {
		return r, false, err
	}

	kind, text, media := "text", m.Message.Text, ""
	if len(m.Message.Attachments) > 0 {
		a := m.Message.Attachments[0]
		kind, media = a.Type, a.Payload.URL
		if text == "" {
			text = attachmentLabel(a.Type)
		}
	} else if m.Message.ReplyTo != nil && m.Message.ReplyTo.Story != nil {
		kind, media = "story_reply", m.Message.ReplyTo.Story.URL
	}
	sender := "contact"
	direction := "in"
	if !inbound {
		sender, direction = "human", "out" // 從 IG App 直接回的
	}

	r.msg = Message{ConversationID: r.convID, Direction: direction, SenderType: sender, Kind: kind, Text: text, MediaURL: media, CreatedAt: ts}
	err := tx.QueryRow(ctx, `
INSERT INTO messages (conversation_id, ig_mid, direction, sender_type, kind, text, media_url, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (ig_mid) DO NOTHING
RETURNING id`, r.convID, m.Message.MID, direction, sender, kind, text, media, ts).Scan(&r.msg.ID)
	if err == pgx.ErrNoRows {
		return r, false, nil // 重複事件（含我方送出後回來的 echo）
	}
	if err != nil {
		return r, false, err
	}

	if inbound {
		_, err = tx.Exec(ctx, `
UPDATE conversations SET unread_count = unread_count + 1, status = 'open',
  last_inbound_at = GREATEST(COALESCE(last_inbound_at, $2), $2),
  last_message_at = GREATEST(COALESCE(last_message_at, $2), $2),
  last_message_text = $3
WHERE id = $1`, r.convID, ts, text)
	} else {
		_, err = tx.Exec(ctx, `
UPDATE conversations SET last_message_at = GREATEST(COALESCE(last_message_at, $2), $2), last_message_text = $3
WHERE id = $1`, r.convID, ts, text)
	}
	return r, true, err
}

func attachmentLabel(t string) string {
	switch t {
	case "image":
		return "[圖片]"
	case "video":
		return "[影片]"
	case "audio":
		return "[語音]"
	case "share":
		return "[分享貼文]"
	case "story_mention":
		return "[在限動提及你]"
	case "ig_reel", "reel":
		return "[分享 Reels]"
	default:
		return "[附件]"
	}
}

// fillProfile 在交易外補抓顧客的 IG 名稱與頭像。
func (s *Service) fillProfile(ctx context.Context, contactID int64, token, igsid string) {
	if token == "" {
		token = s.DefaultToken
	}
	p, err := s.IG.Profile(ctx, token, igsid)
	if err != nil {
		log.Printf("inbox: profile %s: %v", igsid, err)
		return
	}
	if p.Username == "" && p.Name == "" {
		return
	}
	if _, err := s.DB.Exec(ctx, `UPDATE contacts SET username = $2, name = $3, avatar_url = $4 WHERE id = $1`,
		contactID, p.Username, p.Name, p.ProfilePic); err != nil {
		log.Printf("inbox: save profile: %v", err)
	}
}
