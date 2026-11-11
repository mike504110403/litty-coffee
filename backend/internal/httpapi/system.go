package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/mike504110403/litty-coffee/backend/internal/auth"
	"github.com/mike504110403/litty-coffee/backend/internal/monitor"
	"github.com/mike504110403/litty-coffee/backend/internal/realtime"
)

// systemStatus 回系統狀態（所有登入者可看）。安全邊界：帳號清單與 Meta／排程彙總只算登入者看得到的帳號；
// Claude、webhook 積壓、資料庫是全系統的，不含任何帳號資料。
func (s *Server) systemStatus(w http.ResponseWriter, r *http.Request) {
	if s.Monitor == nil {
		writeErr(w, http.StatusServiceUnavailable, "系統狀態未啟用")
		return
	}
	snap := s.Monitor.Latest()
	if snap.CheckedAt.IsZero() {
		snap = s.Monitor.Check(r.Context()) // 剛開機還沒跑過第一輪
	}
	u := currentUser(r)
	v := snap.View(u.CanSeeAccount)
	// 自動回覆開關以 DB 為準（剛切換時不必等下一輪檢查）
	if len(v.Accounts) > 0 {
		ids := make([]int64, len(v.Accounts))
		for i, a := range v.Accounts {
			ids[i] = a.ID
		}
		rows, err := s.DB.Query(r.Context(), `SELECT ig_account_id, auto_reply_enabled FROM automation_settings WHERE ig_account_id = ANY($1)`, ids)
		if err == nil {
			on := map[int64]bool{}
			for rows.Next() {
				var id int64
				var en bool
				if rows.Scan(&id, &en) == nil {
					on[id] = en
				}
			}
			rows.Close()
			for i := range v.Accounts {
				if en, ok := on[v.Accounts[i].ID]; ok {
					v.Accounts[i].AutoReplyEnabled = en
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"checkedAt": v.CheckedAt, "meta": v.Meta, "accounts": v.Accounts, "claude": v.Claude, "jobs": v.Jobs, "db": v.DB,
		"canToggleAutoReply": u.Can(auth.PermAutomation),
	})
}

// systemStreamPing 是 /api/system/stream 的心跳間隔；每次心跳重讀登入者（停用、被踢、權限或可見帳號變更立即生效）。
var systemStreamPing = 25 * time.Second

// systemEventVisible 決定某位登入者收不收得到系統事件：
//   - system.alert：超管、店長、或有自動回覆權限者（Can 已涵蓋超管與店長）；帳號事件還要看得到該帳號（全系統事件 AccountID=0 皆可收）
//   - digest.ready：有分析權限、且看得到該帳號
//   - distill.ready（每週自動蒸餾完成）：有自動回覆權限、且看得到該帳號
func systemEventVisible(u auth.User, e realtime.Event) bool {
	switch e.Type {
	case "system.alert":
		if !u.Can(auth.PermAutomation) {
			return false
		}
		return e.AccountID == 0 || u.CanSeeAccount(e.AccountID)
	case "digest.ready":
		return u.Can(auth.PermAnalytics) && e.AccountID != 0 && u.CanSeeAccount(e.AccountID)
	case "distill.ready":
		return u.Can(auth.PermAutomation) && e.AccountID != 0 && u.CanSeeAccount(e.AccountID)
	}
	return false
}

// systemStream 是總覽／通知用的 SSE（所有登入者可連，與收件匣串流分開，不需收件匣權限）。
func (s *Server) systemStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	var ch <-chan realtime.Event
	if s.SysHub != nil {
		c, unsub := s.SysHub.Subscribe()
		defer unsub()
		ch = c
	}
	_, _ = io.WriteString(w, ": connected\n\n")
	fl.Flush()

	user := currentUser(r)
	ping := time.NewTicker(systemStreamPing)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			u, err := s.Auth.Me(r.Context(), user.ID)
			if err != nil || u.Disabled || !u.SameSession(user) {
				return
			}
			user = u
			_, _ = io.WriteString(w, ": ping\n\n")
			fl.Flush()
		case e := <-ch:
			if !systemEventVisible(user, e) {
				continue
			}
			b, _ := json.Marshal(e.Data)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, b)
			fl.Flush()
		}
	}
}

// ClaudeKeyProbe 回系統狀態用的 Claude key 檢查：Models List（唯讀、不計費），401／403 包成 monitor.ErrKeyInvalid。
// baseURL 空＝官方 API。
func ClaudeKeyProbe(baseURL string) func(ctx context.Context, key string) error {
	return func(ctx context.Context, key string) error {
		opts := []option.RequestOption{option.WithAPIKey(key), option.WithMaxRetries(0)}
		if baseURL != "" {
			opts = append(opts, option.WithBaseURL(baseURL))
		}
		client := anthropic.NewClient(opts...)
		_, err := client.Models.List(ctx, anthropic.ModelListParams{Limit: anthropic.Int(1)})
		if err == nil {
			return nil
		}
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden) {
			return fmt.Errorf("%w（%d）", monitor.ErrKeyInvalid, apiErr.StatusCode)
		}
		if errors.As(err, &apiErr) {
			return fmt.Errorf("Claude API 回應 %d", apiErr.StatusCode)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return errors.New("連線逾時")
		}
		log.Printf("monitor: claude probe: %v", err)
		return errors.New("連不上 Claude API")
	}
}
