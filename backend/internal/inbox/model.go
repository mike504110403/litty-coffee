package inbox

import (
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
)

// InboundHook 在收到顧客新訊息（已落庫）後被呼叫；自動回覆在這裡接。
type InboundHook func(conversationID int64, msg Message)

type Service struct {
	DB           *pgxpool.Pool
	IG           ig.Messenger
	Hub          *realtime.Hub
	OnInbound    InboundHook
	DefaultToken string // ig_accounts 沒存 token 時用（env IG_ACCESS_TOKEN）
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
	Contact         Contact    `json:"contact"`
}

type Message struct {
	ID             int64     `json:"id"`
	ConversationID int64     `json:"conversationId"`
	Direction      string    `json:"direction"`  // in | out
	SenderType     string    `json:"senderType"` // contact | bot | ai | human
	Kind           string    `json:"kind"`
	Text           string    `json:"text"`
	MediaURL       string    `json:"mediaUrl"` // 已存到自有儲存時是 /api/inbox/media/…，否則是 IG 原始網址（可能已過期）
	Reaction       string    `json:"reaction"` // 顧客按的表情
	Deleted        bool      `json:"deleted"`  // 顧客已收回
	CreatedAt      time.Time `json:"createdAt"`
}

func (s *Service) publishMessage(m Message) {
	s.Hub.Publish(realtime.Event{Type: "message.created", Data: m})
}

func (s *Service) publishMessageUpdated(m Message) {
	s.Hub.Publish(realtime.Event{Type: "message.updated", Data: m})
}

func (s *Service) publishConversation(c Conversation) {
	s.Hub.Publish(realtime.Event{Type: "conversation.updated", Data: c})
}
