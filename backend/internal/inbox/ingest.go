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
	accountID int64
	msg       Message
	isInbound bool
}

// connectedAccount 回 webhook entry.id 對應的已連結帳號（存在、啟用、掛著連線）；不是則回 0。
// 未連結的帳號一律不收、也不自動建帳號。
func connectedAccount(ctx context.Context, tx pgx.Tx, igUserID string) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `SELECT id FROM ig_accounts WHERE ig_user_id = $1 AND active AND connection_id IS NOT NULL`, igUserID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// processPayload 在呼叫端的交易內把一個 webhook payload 寫進領域資料表。
// rejected 非空表示有 entry 屬於未連結的帳號（略過不收，事件記這個錯誤）。
func processPayload(ctx context.Context, tx pgx.Tx, raw []byte) (out []ingested, rejected string, err error) {
	var p ig.Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, "", fmt.Errorf("decode payload: %w", err)
	}
	for _, e := range p.Entry {
		accountID, err := connectedAccount(ctx, tx, e.ID)
		if err != nil {
			return nil, "", err
		}
		if accountID == 0 {
			rejected = UnconnectedAccount
			continue
		}
		for _, m := range e.Messaging {
			var (
				r   ingested
				ok  bool
				err error
			)
			switch {
			case m.Read != nil:
				r, ok, err = ingestRead(ctx, tx, accountID, e.ID, m)
			case m.Reaction != nil:
				r, ok, err = ingestReaction(ctx, tx, accountID, e.ID, m)
			case m.Message != nil && m.Message.IsDeleted:
				r, ok, err = ingestUnsend(ctx, tx, accountID, m)
			case m.Message != nil && m.Message.MID != "":
				r, ok, err = ingestMessage(ctx, tx, accountID, m)
			}
			if err != nil {
				return nil, "", err
			}
			if ok {
				r.accountID = accountID
				out = append(out, r)
			}
		}
	}
	return out, rejected, nil
}

// ingestMessage：顧客訊息，或我方在 IG App 直接回的 echo。
// TODO：Unified Login 沒有 message_echoes 訂閱欄位，echo 可能不會再來；處理保留但不依賴它
// （後台送出的訊息在送出當下就落庫了）。
func ingestMessage(ctx context.Context, tx pgx.Tx, accountID int64, m ig.Messaging) (ingested, bool, error) {
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

	r.msg = Message{ConversationID: r.convID, IGAccountID: accountID, Direction: direction, SenderType: sender, Kind: kind, Text: text, MediaURL: media, CreatedAt: ts}
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
func convOf(ctx context.Context, tx pgx.Tx, accountID int64, igsid string) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `
SELECT c.id FROM conversations c JOIN contacts ct ON ct.id = c.contact_id
WHERE ct.ig_account_id = $1 AND ct.igsid = $2`, accountID, igsid).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// ingestRead：顧客已讀。只記時間，之前我方送出的訊息都算已讀。
func ingestRead(ctx context.Context, tx pgx.Tx, accountID int64, accountIGID string, m ig.Messaging) (ingested, bool, error) {
	if m.Sender.ID == accountIGID {
		return ingested{}, false, nil // 我方已讀顧客訊息，後台自己有未讀數
	}
	convID, err := convOf(ctx, tx, accountID, m.Sender.ID)
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
func ingestReaction(ctx context.Context, tx pgx.Tx, accountID int64, accountIGID string, m ig.Messaging) (ingested, bool, error) {
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
	err := tx.QueryRow(ctx, `
UPDATE messages m SET reaction = $2 FROM conversations c, contacts ct
WHERE m.ig_mid = $1 AND c.id = m.conversation_id AND ct.id = c.contact_id AND ct.ig_account_id = $3
RETURNING m.id, m.conversation_id`, m.Reaction.MID, emoji, accountID).Scan(&r.msgID, &r.convID)
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
func ingestUnsend(ctx context.Context, tx pgx.Tx, accountID int64, m ig.Messaging) (ingested, bool, error) {
	r := ingested{event: "updated"}
	ts := time.UnixMilli(m.Timestamp)
	if m.Timestamp == 0 {
		ts = time.Now()
	}
	var created time.Time
	err := tx.QueryRow(ctx, `
UPDATE messages m SET deleted_at = $2, text = '', media_url = '', media_file = '', reaction = ''
FROM (SELECT x.id, x.media_file FROM messages x JOIN conversations c ON c.id = x.conversation_id JOIN contacts ct ON ct.id = c.contact_id
      WHERE x.ig_mid = $1 AND x.direction = 'in' AND x.deleted_at IS NULL AND ct.ig_account_id = $3) old
WHERE m.id = old.id
RETURNING m.id, m.conversation_id, old.media_file, m.created_at`, m.Message.MID, ts, accountID).Scan(&r.msgID, &r.convID, &r.oldFile, &created)
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

// fillProfile 在交易外補抓顧客的 IG 名稱與頭像（用該帳號連線的 token）。
// TODO：graph.facebook.com 上 IGSID profile 的欄位未以真帳號驗證。
func (s *Service) fillProfile(ctx context.Context, contactID, accountID int64, igsid string) {
	_, token, err := s.credentials(ctx, accountID)
	if err != nil {
		log.Printf("inbox: profile %s: %v", igsid, err)
		return
	}
	p, err := s.IG.Profile(ctx, token, igsid)
	if err != nil {
		s.noteAPIError(ctx, accountID, err)
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
