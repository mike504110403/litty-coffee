package automation

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 待改進問答（message_reviews）：
//   - ai_wrong：管理員在收件匣把某則自動回覆（sender_type ai／bot）標「回答有誤」，掛在那則回覆上
//   - ai_unsure：AI 轉真人或模型拒答時自動記錄，掛在顧客那則訊息上
//
// 清單一併帶出「顧客的問題」（ai_unsure＝那則本身；ai_wrong＝回覆之前最近的一則顧客訊息）與「AI 的回覆」。
const (
	ReviewAIWrong  = "ai_wrong"
	ReviewAIUnsure = "ai_unsure"
)

var (
	ErrNotAutoReply  = errors.New("只能標記自動回覆（AI 或關鍵字規則）的訊息")
	ErrNoteRequired  = errors.New("請填寫哪裡有誤")
	ErrAlreadyMarked = errors.New("這則回覆已經標記過，尚未處理")
)

const maxReviewNote = 500

type Review struct {
	ID             int64      `json:"id"`
	Kind           string     `json:"kind"` // ai_wrong | ai_unsure
	IGAccountID    int64      `json:"igAccountId"`
	MessageID      int64      `json:"messageId"`
	ConversationID int64      `json:"conversationId"`
	ContactID      int64      `json:"contactId"`
	ContactName    string     `json:"contactName"` // IG username，沒有就用名稱
	Question       string     `json:"question"`    // 顧客的問題
	Reply          string     `json:"reply"`       // AI／規則的回覆（ai_wrong 才有）
	ReplySender    string     `json:"replySender"` // ai | bot | ''
	Intent         string     `json:"intent"`
	RuleName       string     `json:"ruleName"` // 規則回覆時是哪條規則
	Note           string     `json:"note"`     // ai_wrong＝管理員說明；ai_unsure＝AI 的理由
	CreatedBy      string     `json:"createdBy"`
	CreatedAt      time.Time  `json:"createdAt"`
	ResolvedAt     *time.Time `json:"resolvedAt"`
	ResolvedBy     string     `json:"resolvedBy"`
}

type ReviewFilter struct {
	Accounts []int64 // nil＝全部
	Kind     string  // ai_wrong | ai_unsure | ''
	Status   string  // open | resolved | ''（全部）
	Limit    int
}

// AddUnsure 自動記一筆 ai_unsure（同一則訊息未處理的只留一筆）。
func (s *Store) AddUnsure(ctx context.Context, messageID, accountID int64, note string) error {
	_, err := s.DB.Exec(ctx, `
INSERT INTO message_reviews (message_id, ig_account_id, kind, note) VALUES ($1, $2, 'ai_unsure', $3)
ON CONFLICT (message_id, kind) WHERE resolved_at IS NULL DO NOTHING`, messageID, accountID, clip(note, maxReviewNote))
	return err
}

// AddWrong 管理員標記某則自動回覆有誤。呼叫端先確認訊息在可見範圍、是我方自動回覆（senderType ai／bot）。
func (s *Store) AddWrong(ctx context.Context, messageID, accountID int64, senderType, note string, adminID int64) (Review, error) {
	if senderType != "ai" && senderType != "bot" {
		return Review{}, ErrNotAutoReply
	}
	note = clip(note, maxReviewNote)
	if note == "" {
		return Review{}, ErrNoteRequired
	}
	var id int64
	err := s.DB.QueryRow(ctx, `
INSERT INTO message_reviews (message_id, ig_account_id, kind, note, created_by_admin_id) VALUES ($1, $2, 'ai_wrong', $3, $4)
ON CONFLICT (message_id, kind) WHERE resolved_at IS NULL DO NOTHING
RETURNING id`, messageID, accountID, note, adminID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Review{}, ErrAlreadyMarked
	}
	if err != nil {
		return Review{}, err
	}
	return s.GetReview(ctx, id)
}

const reviewSelect = `
SELECT r.id, r.kind, r.ig_account_id, r.message_id, m.conversation_id, ct.id,
       COALESCE(NULLIF(ct.username, ''), ct.name),
       CASE WHEN m.direction = 'in' THEN m.text ELSE COALESCE((
         SELECT q.text FROM messages q
         WHERE q.conversation_id = m.conversation_id AND q.direction = 'in' AND (q.created_at, q.id) < (m.created_at, m.id)
         ORDER BY q.created_at DESC, q.id DESC LIMIT 1), '') END,
       CASE WHEN m.direction = 'out' THEN m.text ELSE '' END,
       CASE WHEN m.direction = 'out' THEN m.sender_type ELSE '' END,
       COALESCE(m.meta->>'intent', ''), CASE WHEN m.direction = 'out' THEN COALESCE(m.meta->>'ruleName', '') ELSE '' END,
       r.note, COALESCE(NULLIF(cb.display_name, ''), cb.username, ''), r.created_at, r.resolved_at,
       COALESCE(NULLIF(rb.display_name, ''), rb.username, '')
FROM message_reviews r
JOIN messages m ON m.id = r.message_id
JOIN conversations c ON c.id = m.conversation_id
JOIN contacts ct ON ct.id = c.contact_id
LEFT JOIN admin_users cb ON cb.id = r.created_by_admin_id
LEFT JOIN admin_users rb ON rb.id = r.resolved_by_admin_id`

func scanReview(row pgx.Row) (Review, error) {
	var r Review
	err := row.Scan(&r.ID, &r.Kind, &r.IGAccountID, &r.MessageID, &r.ConversationID, &r.ContactID, &r.ContactName,
		&r.Question, &r.Reply, &r.ReplySender, &r.Intent, &r.RuleName, &r.Note, &r.CreatedBy, &r.CreatedAt, &r.ResolvedAt, &r.ResolvedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

func (s *Store) GetReview(ctx context.Context, id int64) (Review, error) {
	return scanReview(s.DB.QueryRow(ctx, reviewSelect+` WHERE r.id = $1`, id))
}

func (s *Store) ListReviews(ctx context.Context, f ReviewFilter) ([]Review, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 200
	}
	rows, err := s.DB.Query(ctx, reviewSelect+`
WHERE ($1::bigint[] IS NULL OR r.ig_account_id = ANY($1))
  AND ($2 = '' OR r.kind = $2)
  AND ($3 = '' OR ($3 = 'open' AND r.resolved_at IS NULL) OR ($3 = 'resolved' AND r.resolved_at IS NOT NULL))
ORDER BY r.created_at DESC, r.id DESC
LIMIT $4`, f.Accounts, f.Kind, f.Status, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Review{}
	for rows.Next() {
		r, err := scanReview(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// OpenReviewCount 回未處理的筆數（儀表板用）。
func (s *Store) OpenReviewCount(ctx context.Context, accounts []int64) (int, error) {
	var n int
	err := s.DB.QueryRow(ctx, `
SELECT count(*) FROM message_reviews WHERE resolved_at IS NULL AND ($1::bigint[] IS NULL OR ig_account_id = ANY($1))`,
		accounts).Scan(&n)
	return n, err
}

// ReviewAccount 回某筆記錄所屬的 IG 帳號（權限檢查用）。
func (s *Store) ReviewAccount(ctx context.Context, id int64) (int64, error) {
	var acc int64
	err := s.DB.QueryRow(ctx, `SELECT ig_account_id FROM message_reviews WHERE id = $1`, id).Scan(&acc)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return acc, err
}

// ResolveReview 標為已處理；已處理過的維持原本的處理時間與處理者。
func (s *Store) ResolveReview(ctx context.Context, id, adminID int64) (Review, error) {
	if _, err := s.DB.Exec(ctx, `
UPDATE message_reviews SET resolved_at = now(), resolved_by_admin_id = $2 WHERE id = $1 AND resolved_at IS NULL`, id, adminID); err != nil {
		return Review{}, err
	}
	return s.GetReview(ctx, id)
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		s = string(r[:n])
	}
	return s
}
