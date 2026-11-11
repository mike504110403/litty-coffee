package analytics

import (
	"context"
	"time"
)

// PeriodBetween 統計 [from, to) 的總量與回應時間（每日摘要用）；accounts 為 nil＝全部帳號。
func (s *Service) PeriodBetween(ctx context.Context, from, to time.Time, accounts []int64) (Period, error) {
	return s.period(ctx, from, to, accounts)
}

// IntentsBetween 回 [from, to) 顧客訊息的意圖分布（多到少）。
func (s *Service) IntentsBetween(ctx context.Context, from, to time.Time, accounts []int64) ([]Count, error) {
	return s.intents(ctx, from, to, accounts)
}
