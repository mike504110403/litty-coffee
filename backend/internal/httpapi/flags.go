package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mike504110403/litty-coffee/backend/internal/auth"
	"github.com/mike504110403/litty-coffee/backend/internal/automation"
	"github.com/mike504110403/litty-coffee/backend/internal/inbox"
)

// 標記與黑名單：
//   - 收件匣（inbox）：POST /messages/{id}/reviews 把自動回覆標「回答有誤」
//   - 自動回覆（automation）：待改進清單 GET /reviews、筆數 GET /reviews/open-count、POST /reviews/{id}/resolve
//   - 顧客標記走 PATCH /contacts/{id}（顧客或收件匣權限，見 patchContact）

func (s *Server) reviewInboxRoutes(r chi.Router) {
	r.Post("/messages/{id}/reviews", s.markMessageWrong)
}

func (s *Server) reviewRoutes(r chi.Router) {
	r.Get("/reviews", s.listReviews)
	r.Get("/reviews/open-count", s.openReviewCount)
	r.Post("/reviews/{id}/resolve", s.resolveReview)
}

func (s *Server) reviewErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, automation.ErrNotFound), errors.Is(err, inbox.ErrNotFound):
		writeErr(w, http.StatusNotFound, "找不到資料")
	case errors.Is(err, automation.ErrNotAutoReply), errors.Is(err, automation.ErrNoteRequired):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, automation.ErrAlreadyMarked):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		s.autoErr(w, err)
	}
}

// markMessageWrong：只能標記看得到的對話裡、我方的自動回覆（AI 或規則）；範圍外與不存在一樣 404。
func (s *Server) markMessageWrong(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	m, err := s.Inbox.GetMessage(r.Context(), id)
	if err == nil && !canSee(r, m.IGAccountID) {
		err = inbox.ErrNotFound
	}
	if err != nil {
		s.reviewErr(w, err)
		return
	}
	rv, err := s.Auto.Store.AddWrong(r.Context(), m.ID, m.IGAccountID, m.SenderType, in.Note, auth.UserID(r.Context()))
	if err != nil {
		s.reviewErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, rv)
}

func (s *Server) listReviews(w http.ResponseWriter, r *http.Request) {
	accounts, ok := accountsFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	kind, status := q.Get("kind"), q.Get("status")
	if kind != "" && kind != automation.ReviewAIWrong && kind != automation.ReviewAIUnsure {
		writeErr(w, http.StatusBadRequest, "kind 必須是 ai_wrong 或 ai_unsure")
		return
	}
	if status != "" && status != "open" && status != "resolved" {
		writeErr(w, http.StatusBadRequest, "status 必須是 open 或 resolved")
		return
	}
	list, err := s.Auto.Store.ListReviews(r.Context(), automation.ReviewFilter{Accounts: accounts, Kind: kind, Status: status})
	if err != nil {
		s.reviewErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) openReviewCount(w http.ResponseWriter, r *http.Request) {
	accounts, ok := accountsFilter(w, r)
	if !ok {
		return
	}
	n, err := s.Auto.Store.OpenReviewCount(r.Context(), accounts)
	if err != nil {
		s.reviewErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": n})
}

func (s *Server) resolveReview(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	acc, err := s.Auto.Store.ReviewAccount(r.Context(), id)
	if err == nil && !canSee(r, acc) {
		err = automation.ErrNotFound
	}
	if err != nil {
		s.reviewErr(w, err)
		return
	}
	rv, err := s.Auto.Store.ResolveReview(r.Context(), id, auth.UserID(r.Context()))
	if err != nil {
		s.reviewErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rv)
}
