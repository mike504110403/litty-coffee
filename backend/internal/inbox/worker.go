package inbox

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
)

// RunWorker 依序處理 webhook_events，直到 ctx 結束。
// 被 EnqueueWebhook 喚醒，另每 5 秒掃一次兜底（例如重啟前沒處理完的）。
func (s *Service) RunWorker(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		for {
			done, err := s.processOne(ctx)
			if err != nil {
				log.Printf("inbox worker: %v", err)
			}
			if done || err != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-tick.C:
		}
	}
}

// processOne 處理一筆待辦事件；沒有待辦時回 done=true。
func (s *Service) processOne(ctx context.Context) (done bool, err error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return true, err
	}
	defer tx.Rollback(ctx)

	var id int64
	var raw []byte
	err = tx.QueryRow(ctx, `
SELECT id, payload FROM webhook_events WHERE processed_at IS NULL
ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&id, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return true, err
	}

	// 用 savepoint 隔開：領域寫入失敗時仍要把事件標成已處理＋記錯誤（可清 processed_at 重播）
	results, perr := func() ([]ingested, error) {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return nil, err
		}
		res, err := processPayload(ctx, sp, raw)
		if err != nil {
			_ = sp.Rollback(ctx)
			return nil, err
		}
		return res, sp.Commit(ctx)
	}()
	errText := ""
	if perr != nil {
		errText = perr.Error()
		log.Printf("inbox: event %d failed: %v", id, perr)
	}
	if _, err := tx.Exec(ctx, `UPDATE webhook_events SET processed_at = now(), error = $2 WHERE id = $1`, id, errText); err != nil {
		return true, err
	}
	if err := tx.Commit(ctx); err != nil {
		return true, err
	}

	for _, r := range results {
		if r.needsProf {
			s.fillProfile(ctx, r.contactID, r.token, r.igsid)
		}
		s.publishMessage(r.msg)
		if c, err := s.GetConversation(ctx, r.convID); err == nil {
			s.publishConversation(c)
		}
		if r.isInbound && s.OnInbound != nil {
			s.OnInbound(r.convID, r.msg)
		}
	}
	return false, nil
}

// ProcessPending 同步處理所有待辦事件，回傳處理筆數（測試與手動重播用）。
func (s *Service) ProcessPending(ctx context.Context) (int, error) {
	n := 0
	for {
		done, err := s.processOne(ctx)
		if err != nil || done {
			return n, err
		}
		n++
	}
}
