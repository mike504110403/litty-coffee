package ig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Payload 是 Instagram messaging webhook 的外層結構。
type Payload struct {
	Object string  `json:"object"`
	Entry  []Entry `json:"entry"`
}

type Entry struct {
	ID        string      `json:"id"` // 我方 IG 專業帳號 ID
	Time      int64       `json:"time"`
	Messaging []Messaging `json:"messaging"`
}

type Messaging struct {
	Sender    IDRef     `json:"sender"`
	Recipient IDRef     `json:"recipient"`
	Timestamp int64     `json:"timestamp"` // ms
	Message   *Message  `json:"message,omitempty"`
	Read      *Read     `json:"read,omitempty"`     // messaging_seen：對方已讀到 mid
	Reaction  *Reaction `json:"reaction,omitempty"` // message_reactions
}

type Read struct {
	MID string `json:"mid"`
}

type Reaction struct {
	MID      string `json:"mid"`
	Action   string `json:"action"`   // react | unreact
	Reaction string `json:"reaction"` // love 等代號
	Emoji    string `json:"emoji"`
}

type IDRef struct {
	ID string `json:"id"`
}

type Message struct {
	MID         string       `json:"mid"`
	Text        string       `json:"text,omitempty"`
	IsEcho      bool         `json:"is_echo,omitempty"`
	IsDeleted   bool         `json:"is_deleted,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	ReplyTo     *struct {
		MID   string `json:"mid,omitempty"`
		Story *struct {
			URL string `json:"url"`
			ID  string `json:"id"`
		} `json:"story,omitempty"`
	} `json:"reply_to,omitempty"`
}

type Attachment struct {
	Type    string `json:"type"` // image | video | audio | file | share | story_mention | ...
	Payload struct {
		URL string `json:"url"`
	} `json:"payload"`
}

// VerifySignature 檢查 X-Hub-Signature-256（sha256=<hex hmac>）。
func VerifySignature(appSecret string, body []byte, header string) bool {
	if appSecret == "" || !strings.HasPrefix(header, "sha256=") {
		return false
	}
	got, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}
