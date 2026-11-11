package inbox

import (
	"context"
	"encoding/json"
	"errors"
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

// ingested 是一個 webhook 事件處理後要在交易外做的事。
type ingested struct {
	event     string // created（新訊息）| updated（表情、收回）| read（已讀）
	msgID     int64  // updated 用
	oldFile   string // 收回時要刪掉的本地附件
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
			var (
				r   ingested
				ok  bool
				err error
			)
			switch {
			case m.Read != nil:
				r, ok, err = ingestRead(ctx, tx, e.ID, m)
			case m.Reaction != nil:
				r, ok, err = ingestReaction(ctx, tx, e.ID, m)
			case m.Message != nil && m.Message.IsDeleted:
				r, ok, err = ingestUnsend(ctx, tx, m)
			case m.Message != nil && m.Message.MID != "":
				r, ok, err = ingestMessage(ctx, tx, e.ID, m)
			}
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
	r := ingested{event: "created"}
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

// convOf 找出（我方帳號, 顧客 igsid）對應的對話；沒有就回 0。
func convOf(ctx context.Context, tx pgx.Tx, accountIGID, igsid string) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `
SELECT c.id FROM conversations c JOIN contacts ct ON ct.id = c.contact_id JOIN ig_accounts a ON a.id = ct.ig_account_id
WHERE a.ig_user_id = $1 AND ct.igsid = $2`, accountIGID, igsid).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// ingestRead：顧客已讀。只記時間，之前我方送出的訊息都算已讀。
func ingestRead(ctx context.Context, tx pgx.Tx, accountIGID string, m ig.Messaging) (ingested, bool, error) {
	if m.Sender.ID == accountIGID {
		return ingested{}, false, nil // 我方已讀顧客訊息，後台自己有未讀數
	}
	convID, err := convOf(ctx, tx, accountIGID, m.Sender.ID)
	if err != nil || convID == 0 {
		return ingested{}, false, err
	}
	ts := time.UnixMilli(m.Timestamp)
	if m.Timestamp == 0 {
		ts = time.Now()
	}
	_, err = tx.Exec(ctx, `UPDATE conversations SET contact_read_at = GREATEST(COALESCE(contact_read_at, $2), $2) WHERE id = $1`, convID, ts)
	return ingested{event: "read", convID: convID}, err == nil, err
}

// ingestReaction：只記顧客按的表情（我方在 IG App 按的不顯示）。
func ingestReaction(ctx context.Context, tx pgx.Tx, accountIGID string, m ig.Messaging) (ingested, bool, error) {
	if m.Sender.ID == accountIGID || m.Reaction.MID == "" {
		return ingested{}, false, nil
	}
	emoji := ""
	if m.Reaction.Action != "unreact" {
		emoji = m.Reaction.Emoji
		if emoji == "" {
			emoji = reactionEmoji(m.Reaction.Reaction)
		}
	}
	r := ingested{event: "updated"}
	err := tx.QueryRow(ctx, `UPDATE messages SET reaction = $2 WHERE ig_mid = $1 RETURNING id, conversation_id`,
		m.Reaction.MID, emoji).Scan(&r.msgID, &r.convID)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

func reactionEmoji(code string) string {
	switch code {
	case "love":
		return "❤️"
	case "like":
		return "👍"
	case "laugh", "haha":
		return "😂"
	case "wow":
		return "😮"
	case "sad":
		return "😢"
	case "angry":
		return "😡"
	}
	return "❤️"
}

// ingestUnsend：顧客收回。清掉內容（尊重對方收回），只留「已收回」紀錄。
func ingestUnsend(ctx context.Context, tx pgx.Tx, m ig.Messaging) (ingested, bool, error) {
	r := ingested{event: "updated"}
	ts := time.UnixMilli(m.Timestamp)
	if m.Timestamp == 0 {
		ts = time.Now()
	}
	var created time.Time
	err := tx.QueryRow(ctx, `
UPDATE messages m SET deleted_at = $2, text = '', media_url = '', media_file = '', reaction = ''
FROM (SELECT id, media_file FROM messages WHERE ig_mid = $1 AND direction = 'in' AND deleted_at IS NULL) old
WHERE m.id = old.id
RETURNING m.id, m.conversation_id, old.media_file, m.created_at`, m.Message.MID, ts).Scan(&r.msgID, &r.convID, &r.oldFile, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	_, err = tx.Exec(ctx, `UPDATE conversations SET last_message_text = '[已收回訊息]' WHERE id = $1 AND last_message_at = $2`, r.convID, created)
	return r, err == nil, err
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
