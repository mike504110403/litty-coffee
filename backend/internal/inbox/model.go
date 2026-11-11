package inbox

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mike504110403/litty-coffee/backend/internal/ig"
	"github.com/mike504110403/litty-coffee/backend/internal/realtime"
)

const (
	ReplyWindow      = 24 * time.Hour
	HumanAgentWindow = 7 * 24 * time.Hour
)

var (
	ErrNotFound     = errors.New("conversation not found")
	ErrWindowClosed = errors.New("已超過 IG 回覆窗口，需等顧客再傳訊息才能回覆")
	ErrEmptyText    = errors.New("訊息不能是空的")
	// ErrCannotSend 包住「這個帳號現在不能送」的原因（停用、未連結、授權過期）
	ErrCannotSend = errors.New("無法從這個 IG 帳號送出")
)

// UnconnectedAccount 是未連結（不在 ig_accounts、停用或沒掛連線）帳號的 webhook 事件錯誤訊息。
const UnconnectedAccount = "未連結的帳號"

// Credentials 回某帳號（ig_accounts.id）發 API 用的 IG 帳號 ID 與 token；正式環境是 account.Service.Credentials。
type Credentials interface {
	Credentials(ctx context.Context, accountID int64) (igUserID, token string, err error)
}

// APIErrorNoter 選配：Credentials 若也實作它，IG API 錯誤會回報（token 失效時記在連線上）。
type APIErrorNoter interface {
	NoteAPIError(ctx context.Context, accountID int64, err error)
}

// InboundHook 在收到顧客新訊息（已落庫）後被呼叫；自動回覆在這裡接。
type InboundHook func(conversationID int64, msg Message)

type Service struct {
	DB           *pgxpool.Pool
	IG           ig.Messenger
	Hub          *realtime.Hub
	OnInbound    InboundHook
	Creds        Credentials // 每個帳號用自己連線的 token；nil 時用 DefaultToken（測試）
	DefaultToken string
	MediaDir     string // 收到的附件存到 MediaDir/inbox/（IG 的 CDN 網址會過期）
	Download     *http.Client
	wake         chan struct{}
}

func NewService(db *pgxpool.Pool, m ig.Messenger, hub *realtime.Hub) *Service {
	return &Service{DB: db, IG: m, Hub: hub, Download: &http.Client{Timeout: 60 * time.Second}, wake: make(chan struct{}, 1)}
}

type Contact struct {
	ID        int64  `json:"id"`
	IGSID     string `json:"igsid"`
	Username  string `json:"username"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatarUrl"`
	// 標記：'' 一般｜watch 問題顧客｜blocked 黑名單（AI 不回、不發轉真人通知）
	Risk       string     `json:"risk"`
	RiskReason string     `json:"riskReason"`
	RiskSource string     `json:"riskSource"` // manual | rate | ai
	RiskAt     *time.Time `json:"riskAt"`
	// 降頻等級：0＝正常；N≥1＝自動回覆每 5×2^(N-1) 分鐘最多一次（短時間大量訊息時啟動）
	ThrottleLevel int `json:"throttleLevel"`
}

type Conversation struct {
	ID              int64      `json:"id"`
	Status          string     `json:"status"`
	Handler         string     `json:"handler"`
	UnreadCount     int        `json:"unreadCount"`
	LastMessageAt   *time.Time `json:"lastMessageAt"`
	LastInboundAt   *time.Time `json:"lastInboundAt"`
	LastMessageText string     `json:"lastMessageText"`
	WindowExpiresAt *time.Time `json:"windowExpiresAt"` // 24h 標準窗口
	ContactReadAt   *time.Time `json:"contactReadAt"`   // 顧客最後已讀時間（之前送出的訊息都算已讀）
	IGAccountID     int64      `json:"igAccountId"`     // 這段對話屬於哪個我方 IG 帳號
	Contact         Contact    `json:"contact"`
}

type Message struct {
	ID             int64     `json:"id"`
	ConversationID int64     `json:"conversationId"`
	IGAccountID    int64     `json:"igAccountId"`
	Direction      string    `json:"direction"`  // in | out
	SenderType     string    `json:"senderType"` // contact | bot | ai | human | system（系統事件，不送 IG）
	Kind           string    `json:"kind"`
	Text           string    `json:"text"`
	MediaURL       string    `json:"mediaUrl"` // 已存到自有儲存時是 /api/inbox/media/…，否則是 IG 原始網址（可能已過期）
	Reaction       string    `json:"reaction"` // 顧客按的表情
	Deleted        bool      `json:"deleted"`  // 顧客已收回
	SentBy         *Sender   `json:"sentBy"`   // 後台真人送出時是哪位管理員；自動回覆、從 IG app 回的為 null
	CreatedAt      time.Time `json:"createdAt"`
}

// Sender 是送出訊息的管理員。
type Sender struct {
	ID          int64  `json:"id"`
	DisplayName string `json:"displayName"`
}

func (s *Service) publishMessage(m Message) {
	s.Hub.Publish(realtime.Event{Type: "message.created", Data: m, AccountID: m.IGAccountID})
}

func (s *Service) publishMessageUpdated(m Message) {
	s.Hub.Publish(realtime.Event{Type: "message.updated", Data: m, AccountID: m.IGAccountID})
}

// HandoffEvent 是轉真人通知的內容。
type HandoffEvent struct {
	Conversation Conversation `json:"conversation"`
	Reason       string       `json:"reason"`
}

func (s *Service) publishHandoff(c Conversation, reason string) {
	s.Hub.Publish(realtime.Event{Type: "handoff.requested", Data: HandoffEvent{Conversation: c, Reason: reason}, AccountID: c.IGAccountID})
}

func (s *Service) publishConversation(c Conversation) {
	s.Hub.Publish(realtime.Event{Type: "conversation.updated", Data: c, AccountID: c.IGAccountID})
}

// credentials 取某帳號的 (IG 帳號 ID, token)。
func (s *Service) credentials(ctx context.Context, accountID int64) (string, string, error) {
	if s.Creds != nil {
		return s.Creds.Credentials(ctx, accountID)
	}
	var igUserID string
	if err := s.DB.QueryRow(ctx, `SELECT ig_user_id FROM ig_accounts WHERE id = $1`, accountID).Scan(&igUserID); err != nil {
		return "", "", err
	}
	return igUserID, s.DefaultToken, nil
}

func (s *Service) noteAPIError(ctx context.Context, accountID int64, err error) {
	if n, ok := s.Creds.(APIErrorNoter); ok {
		n.NoteAPIError(ctx, accountID, err)
	}
}
