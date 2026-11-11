package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mike504110403/litty-coffee/backend/internal/automation"
	"github.com/mike504110403/litty-coffee/backend/internal/media"
	"github.com/mike504110403/litty-coffee/backend/internal/posts"
)

func (s *Server) postRoutes(r chi.Router) {
	r.Post("/media", s.uploadMedia)
	r.Get("/posts", s.listPosts)
	r.Get("/posts/upcoming-count", s.upcomingCount)
	r.Post("/posts", s.savePost)
	r.Get("/posts/{id}", s.getPost)
	r.Put("/posts/{id}", s.savePost)
	r.Delete("/posts/{id}", s.deletePost)
	r.Post("/posts/caption", s.suggestCaption)
	r.Post("/posts/{id}/insights/refresh", s.refreshInsights)
}

// uploadMedia：表單欄位 account＝這個檔案要給哪個 IG 帳號用（必填、須在可見範圍內）。
func (s *Server) uploadMedia(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, media.MaxVideoBytes+(1<<20))
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "請選擇檔案（檔案可能超過 100MB）")
		return
	}
	defer f.Close()
	acc, _ := strconv.ParseInt(r.FormValue("account"), 10, 64)
	if !requireAccount(w, r, acc) {
		return
	}
	a, err := s.Media.Save(r.Context(), acc, hdr.Filename, f)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func parseTime(v string) *time.Time {
	if v == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil
	}
	return &t
}

func (s *Server) listPosts(w http.ResponseWriter, r *http.Request) {
	accounts, ok := accountsFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	list, err := s.Posts.List(r.Context(), posts.ListFilter{From: parseTime(q.Get("from")), To: parseTime(q.Get("to")),
		Status: q.Get("status"), Accounts: accounts})
	if err != nil {
		log.Printf("posts: %v", err)
		writeErr(w, http.StatusInternalServerError, "讀取失敗")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) upcomingCount(w http.ResponseWriter, r *http.Request) {
	accounts, ok := accountsFilter(w, r)
	if !ok {
		return
	}
	n, err := s.Posts.CountUpcoming(r.Context(), accounts)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "讀取失敗")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": n})
}

// scopedPost 讀貼文；不在可見範圍內一律回 ErrNotFound。
func (s *Server) scopedPost(r *http.Request, id int64) (posts.Post, error) {
	p, err := s.Posts.Get(r.Context(), id)
	if err == nil && !canSee(r, p.IGAccountID) {
		return posts.Post{}, posts.ErrNotFound
	}
	return p, err
}

func (s *Server) getPost(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	p, err := s.scopedPost(r, id)
	if err != nil {
		s.postErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) postErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, posts.ErrNotFound):
		writeErr(w, http.StatusNotFound, posts.ErrNotFound.Error())
	case errors.Is(err, posts.ErrImmutable):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		log.Printf("posts: %v", err)
		writeErr(w, http.StatusInternalServerError, "伺服器錯誤")
	}
}

func (s *Server) savePost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Post   posts.Post `json:"post"`
		Action string     `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	in.Post.ID = 0
	if chi.URLParam(r, "id") != "" {
		id, err := idParam(r)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad id")
			return
		}
		old, err := s.scopedPost(r, id)
		if err != nil {
			s.postErr(w, err)
			return
		}
		in.Post.ID = id
		if in.Post.IGAccountID == 0 {
			in.Post.IGAccountID = old.IGAccountID
		}
	}
	if !requireAccount(w, r, in.Post.IGAccountID) {
		return
	}
	p, err := s.Posts.Save(r.Context(), in.Post, in.Action, time.Now())
	switch {
	case errors.Is(err, posts.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, posts.ErrImmutable):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, p)
	}
}

func (s *Server) deletePost(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if _, err := s.scopedPost(r, id); err != nil {
		s.postErr(w, err)
		return
	}
	err = s.Posts.Delete(r.Context(), id)
	switch {
	case errors.Is(err, posts.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, posts.ErrImmutable):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "刪除失敗")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// suggestCaption 用指定帳號的人設與知識庫寫文案（body.account 必填、須在可見範圍內）。
func (s *Server) suggestCaption(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Brief    string  `json:"brief"`
		Kind     string  `json:"kind"`
		Account  int64   `json:"account"`
		MediaIDs []int64 `json:"mediaIds"` // 貼文的圖片：一併給 AI 看（最多 captionMaxImages 張，只取圖片）
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || (strings.TrimSpace(in.Brief) == "" && len(in.MediaIDs) == 0) {
		writeErr(w, http.StatusBadRequest, "請描述這篇貼文想說什麼，或先加入圖片")
		return
	}
	if !requireAccount(w, r, in.Account) {
		return
	}
	if s.CaptionAI == nil || !s.CaptionAI.Enabled() {
		writeErr(w, http.StatusServiceUnavailable, "尚未設定 Claude API Key，無法使用 AI 寫文案")
		return
	}
	st, err := s.Auto.Store.GetSettings(r.Context(), in.Account)
	if errors.Is(err, automation.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "找不到這個 IG 帳號")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "讀取設定失敗")
		return
	}
	kb, err := s.Auto.Store.ListKnowledge(r.Context(), in.Account)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "讀取知識庫失敗")
		return
	}
	images, err := s.captionImages(r.Context(), in.Account, in.MediaIDs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	caption, err := s.CaptionAI.Caption(r.Context(), st, kb, in.Brief, in.Kind, images)
	if err != nil {
		log.Printf("caption: %v", err)
		writeErr(w, http.StatusBadGateway, "AI 呼叫失敗："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"caption": caption, "hashtags": posts.CountHashtags(caption)})
}

const (
	captionMaxImages     = 4
	captionMaxImageBytes = 4 << 20 // Claude 單張圖片上限約 5MB（base64 前），超過的略過
)

// captionImages 讀出要給 AI 看的貼文圖片：必須屬於同一個 IG 帳號（避免拿別帳號的媒體）；影片略過。
func (s *Server) captionImages(ctx context.Context, account int64, ids []int64) ([]automation.CaptionImage, error) {
	if len(ids) == 0 || s.Media == nil {
		return nil, nil
	}
	assets, err := s.Media.Get(ctx, ids)
	if err != nil {
		return nil, errors.New("讀取媒體失敗")
	}
	byID := make(map[int64]media.Asset, len(assets))
	for _, a := range assets {
		byID[a.ID] = a
	}
	var out []automation.CaptionImage
	for _, id := range ids { // 依貼文順序
		a, ok := byID[id]
		if !ok || a.IGAccountID != account {
			return nil, errors.New("媒體不存在或不屬於這個 IG 帳號")
		}
		if a.Kind != "image" || len(out) >= captionMaxImages {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.Media.Dir, filepath.Base(a.Filename)))
		if err != nil || len(data) > captionMaxImageBytes {
			continue
		}
		mt := a.ContentType
		if mt != "image/png" && mt != "image/gif" && mt != "image/webp" {
			mt = "image/jpeg"
		}
		out = append(out, automation.CaptionImage{Data: data, MediaType: mt})
	}
	return out, nil
}

// refreshInsights 手動更新單篇已發佈貼文的成效；成功回更新後的貼文。
func (s *Server) refreshInsights(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if _, err := s.scopedPost(r, id); err != nil {
		s.postErr(w, err)
		return
	}
	if s.Posts.Insights == nil {
		writeErr(w, http.StatusServiceUnavailable, "成效數據未啟用")
		return
	}
	p, err := s.Posts.Insights.RefreshPost(r.Context(), id)
	switch {
	case errors.Is(err, posts.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, posts.ErrInsightsUnavailable), errors.Is(err, posts.ErrNoToken):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		log.Printf("insights refresh %d: %v", id, err)
		writeErr(w, http.StatusBadGateway, "向 IG 取得數據失敗："+err.Error())
	default:
		writeJSON(w, http.StatusOK, p)
	}
}
