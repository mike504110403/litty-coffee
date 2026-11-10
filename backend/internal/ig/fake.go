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
}

func NewFake() *Fake { return &Fake{profiles: map[string]Profile{}} }

func (f *Fake) Register(igsid string, p Profile) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.profiles[igsid] = p
}

func (f *Fake) SendText(_ context.Context, _, _, _, _ string, _ bool) (string, error) {
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
