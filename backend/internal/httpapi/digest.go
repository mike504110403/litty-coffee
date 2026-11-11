package httpapi

import (
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mike504110403/litty-coffee/backend/internal/digest"
)

// 每日摘要（分析權限）：一律指定單一帳號（缺 400、範圍外 403）。
func (s *Server) digestRoutes(r chi.Router) {
	r.Get("/digests/latest", s.latestDigest)
	r.Post("/digests/generate", s.generateDigest)
}

// latestDigest 回帳號最新一份摘要；還沒有時回 200 null（前端顯示「還沒有摘要」）。
func (s *Server) latestDigest(w http.ResponseWriter, r *http.Request) {
	acc, ok := accountQuery(w, r)
	if !ok {
		return
	}
	if s.Digests == nil {
		writeErr(w, http.StatusServiceUnavailable, "每日摘要未啟用")
		return
	}
	d, err := s.Digests.Latest(r.Context(), acc)
	if errors.Is(err, digest.ErrNotFound) {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	if err != nil {
		log.Printf("digest latest: %v", err)
		writeErr(w, http.StatusInternalServerError, "伺服器錯誤")
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// generateDigest 手動重新產生某天的摘要（?date= 省略＝昨天，店家時區），覆蓋既有的那份。
func (s *Server) generateDigest(w http.ResponseWriter, r *http.Request) {
	acc, ok := accountQuery(w, r)
	if !ok {
		return
	}
	if s.Digests == nil {
		writeErr(w, http.StatusServiceUnavailable, "每日摘要未啟用")
		return
	}
	date, err := s.Digests.ParseDate(r.URL.Query().Get("date"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var exists bool
	if err := s.DB.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM ig_accounts WHERE id = $1)`, acc).Scan(&exists); err != nil || !exists {
		writeErr(w, http.StatusNotFound, "找不到這個 IG 帳號")
		return
	}
	d, err := s.Digests.Generate(r.Context(), acc, date)
	if err != nil {
		log.Printf("digest generate: %v", err)
		writeErr(w, http.StatusInternalServerError, "產生摘要失敗")
		return
	}
	writeJSON(w, http.StatusOK, d)
}
