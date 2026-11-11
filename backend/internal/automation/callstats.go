package automation

import (
	"errors"
	"sync"
	"time"
)

// CallRecord 是一次真的打到 Claude API 的呼叫結果（系統狀態面板用）。
type CallRecord struct {
	Kind       string    `json:"kind"` // reply（自動回覆／沙盒）| digest（每日摘要）| suggest（收件匣 AI 建議回覆）
	At         time.Time `json:"at"`
	OK         bool      `json:"ok"`
	Error      string    `json:"error,omitempty"`
	DurationMs int64     `json:"durationMs"`
}

// CallStats 記最近一次 AI 呼叫與連續失敗次數；並行安全，nil 接收者＝不記。
type CallStats struct {
	mu    sync.Mutex
	last  *CallRecord
	fails int
}

// Record 記一次呼叫；模型拒答（ErrRefused）是 API 正常回應，算成功。
func (c *CallStats) Record(kind string, start time.Time, err error) {
	if c == nil {
		return
	}
	r := CallRecord{Kind: kind, At: time.Now(), OK: err == nil || errors.Is(err, ErrRefused),
		DurationMs: time.Since(start).Milliseconds()}
	if !r.OK {
		r.Error = clip(err.Error(), 200)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last = &r
	if r.OK {
		c.fails = 0
	} else {
		c.fails++
	}
}

// Last 回最近一次呼叫（沒有＝nil）與目前連續失敗次數。
func (c *CallStats) Last() (*CallRecord, int) {
	if c == nil {
		return nil, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil {
		return nil, c.fails
	}
	r := *c.last
	return &r, c.fails
}
