package ig

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
)

// Fake 是開發期的假 IG：送訊只回一個假 mid，查人從模擬器登記的資料拿。
type Fake struct {
	mu       sync.RWMutex
	profiles map[string]Profile
	sent     []SentCall
}

// SentCall 是 Fake 收到的一次送訊（測試驗證從哪個帳號、用哪個 token 送）。
type SentCall struct{ AccountID, Token, IGSID, Text string }

// Sent 回目前為止的送訊紀錄複本。
func (f *Fake) Sent() []SentCall {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return append([]SentCall(nil), f.sent...)
}

func NewFake() *Fake { return &Fake{profiles: map[string]Profile{}} }

func (f *Fake) Register(igsid string, p Profile) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.profiles[igsid] = p
}

func (f *Fake) SendText(_ context.Context, accountID, token, igsid, text string, _ bool) (string, error) {
	f.mu.Lock()
	f.sent = append(f.sent, SentCall{AccountID: accountID, Token: token, IGSID: igsid, Text: text})
	f.mu.Unlock()
	return "dev-mid-" + randHex(8), nil
}

func (f *Fake) Profile(_ context.Context, _, igsid string) (Profile, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.profiles[igsid], nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
