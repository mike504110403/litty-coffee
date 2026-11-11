package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mike504110403/litty-coffee/backend/internal/auth"
	"github.com/mike504110403/litty-coffee/backend/internal/automation"
)

// 蒸餾自動回覆（自動回覆權限）：把近期對話整理成「建議的」規則／知識庫，店家審核後才生效。
//   - POST /automation/distill?account=&days=：同步跑一次（≤120 秒）；同帳號並行 409、10 分鐘一次 429、沒 key 503
//   - GET  /automation/distill/runs?account=、/automation/distill/suggestions?account=&status=
//   - POST /automation/distill/suggestions/{id}/accept（可帶編輯內容）、/dismiss：範圍外 404
func (s *Server) distillRoutes(r chi.Router) {
	r.Post("/distill", s.startDistill)
	r.Get("/distill/runs", s.listDistillRuns)
	r.Get("/distill/suggestions", s.listDistillSuggestions)
	r.Post("/distill/suggestions/{id}/accept", s.acceptDistill)
	r.Post("/distill/suggestions/{id}/dismiss", s.dismissDistill)
}

// distillAI 回目前可用的蒸餾 AI：測試可換 DistillAI，正式沿用同一個 LiveAI；未啟用回 nil。
func (s *Server) distillAI() automation.DistillAI {
	var a automation.DistillAI
	switch {
	case s.DistillAI != nil:
		a = s.DistillAI
	case s.CaptionAI != nil:
		a = s.CaptionAI
	default:
		return nil
	}
	if !a.Enabled() {
		return nil
	}
	return a
}

func (s *Server) startDistill(w http.ResponseWriter, r *http.Request) {
	acc, ok := accountQuery(w, r)
	if !ok {
		return
	}
	days := automation.DistillDefaultDays
	if v := r.URL.Query().Get("days"); v != "" {
		d, err := strconv.Atoi(v)
		if err != nil || !automation.ValidDistillDays(d) {
			writeErr(w, http.StatusBadRequest, "範圍只能是 7、30 或 90 天")
			return
		}
		days = d
	}
	ai := s.distillAI()
	if ai == nil {
		writeErr(w, http.StatusServiceUnavailable, automation.ErrAIDisabled.Error())
		return
	}
	d := &automation.Distiller{Store: s.Auto.Store, AI: ai, Calls: s.Auto.Calls}
	// 管理員關掉頁面也讓這次跑完（結果會留在建議清單）；整次上限 DistillTimeout＋讀寫 DB 的餘裕
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), automation.DistillTimeout+15*time.Second)
	defer cancel()
	adminID := auth.UserID(r.Context())
	start := time.Now()
	run, err := d.Start(ctx, acc, days, "manual", adminID)
	// 只記結果與耗時，不記素材與建議內容
	log.Printf("distill: account=%d admin=%d days=%d model=%s status=%s suggestions=%d chars=%d elapsed_ms=%d err=%v",
		acc, adminID, days, ai.Model(), run.Status, run.SuggestionCount, run.InputChars, time.Since(start).Milliseconds(), err)
	var tooSoon *automation.DistillTooSoonError
	var invalid *automation.DistillInvalidError
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"run": run})
	case errors.As(err, &tooSoon):
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(tooSoon.Wait.Seconds()))))
		writeErr(w, http.StatusTooManyRequests, err.Error())
	case errors.As(err, &invalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, automation.ErrDistillRunning):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, automation.ErrAIDisabled):
		writeErr(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, automation.ErrNotFound):
		s.autoErr(w, err)
	case errors.Is(err, automation.ErrRefused):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "AI 拒絕處理這批對話，請換個範圍再試", "run": run})
	case errors.Is(err, context.DeadlineExceeded):
		writeJSON(w, http.StatusGatewayTimeout, map[string]any{"error": "蒸餾逾時，請縮小範圍再試", "run": run})
	case run.ID != 0:
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "AI 呼叫失敗，請稍後再試", "run": run})
	default:
		s.autoErr(w, err)
	}
}

func (s *Server) listDistillRuns(w http.ResponseWriter, r *http.Request) {
	acc, ok := accountQuery(w, r)
	if !ok {
		return
	}
	list, err := s.Auto.Store.ListDistillRuns(r.Context(), acc, 10)
	if err != nil {
		s.autoErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) listDistillSuggestions(w http.ResponseWriter, r *http.Request) {
	acc, ok := accountQuery(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "", "pending", "accepted", "dismissed":
	case "all":
		status = ""
	default:
		writeErr(w, http.StatusBadRequest, "status 只能是 pending、accepted、dismissed")
		return
	}
	list, err := s.Auto.Store.ListDistillItems(r.Context(), acc, status)
	if err != nil {
		s.autoErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// scopedDistill 取 {id} 的建議並檢查可見範圍：不存在或範圍外一律 404。
func (s *Server) scopedDistill(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return 0, false
	}
	acc, err := s.Auto.Store.DistillItemAccount(r.Context(), id)
	if errors.Is(err, automation.ErrNotFound) || (err == nil && !canSee(r, acc)) {
		writeErr(w, http.StatusNotFound, "找不到這筆建議")
		return 0, false
	}
	if err != nil {
		s.autoErr(w, err)
		return 0, false
	}
	return id, true
}

func (s *Server) distillDecisionErr(w http.ResponseWriter, err error) {
	var invalid *automation.DistillInvalidError
	switch {
	case errors.Is(err, automation.ErrNeedsInput), errors.As(err, &invalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, automation.ErrDistillDecided), errors.Is(err, automation.ErrDistillGone):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		s.autoErr(w, err)
	}
}

func (s *Server) acceptDistill(w http.ResponseWriter, r *http.Request) {
	id, ok := s.scopedDistill(w, r)
	if !ok {
		return
	}
	// body 可省略（直接採用）；有帶 rule／knowledge＝編輯後採用
	var edit automation.DistillEdit
	if err := json.NewDecoder(r.Body).Decode(&edit); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	var ep *automation.DistillEdit
	if edit.Rule != nil || edit.Knowledge != nil {
		ep = &edit
	}
	it, err := s.Auto.Store.AcceptDistill(r.Context(), id, auth.UserID(r.Context()), ep)
	if err != nil {
		s.distillDecisionErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

func (s *Server) dismissDistill(w http.ResponseWriter, r *http.Request) {
	id, ok := s.scopedDistill(w, r)
	if !ok {
		return
	}
	it, err := s.Auto.Store.DismissDistill(r.Context(), id, auth.UserID(r.Context()))
	if err != nil {
		s.distillDecisionErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, it)
}
