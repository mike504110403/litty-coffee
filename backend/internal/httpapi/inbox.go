package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mike504110403/litty-coffee/backend/internal/auth"
	"github.com/mike504110403/litty-coffee/backend/internal/ig"
	"github.com/mike504110403/litty-coffee/backend/internal/inbox"
)

func (s *Server) inboxRoutes(r chi.Router) {
	r.Get("/conversations", s.listConversations)
	r.Get("/conversations/{id}", s.getConversation)
	r.Patch("/conversations/{id}", s.patchConversation)
	r.Post("/conversations/{id}/read", s.readConversation)
	r.Get("/conversations/{id}/messages", s.listMessages)
	r.Post("/conversations/{id}/messages", s.sendMessage)
	r.Post("/conversations/{id}/suggest", s.suggestReply)    // AI 建議回覆：只回草稿，不送出
	r.Get("/conversations/{id}/suggestion", s.getSuggestion) // 背景預先產生的建議（不呼叫 AI、不計限流）
	r.Get("/inbox/stream", s.inboxStream)
	r.Get("/inbox/ai", s.suggestStatus) // 收件匣用的 AI 狀態（只回有沒有 key；/automation/status 需要自動回覆權限）
	r.Get("/inbox/media/{name}", s.inboxMedia)
}

// inboxMedia 只給看得到該對話帳號的人；範圍外與不存在一樣回 404。
func (s *Server) inboxMedia(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	acc, err := s.Inbox.MediaAccount(r.Context(), name)
	if errors.Is(err, inbox.ErrNotFound) || (err == nil && !canSee(r, acc)) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.inboxErr(w, err)
		return
	}
	s.Inbox.MediaHandler(w, r, name)
}

// scopedConv 解析 {id} 並確認對話在登入者的可見範圍內（範圍外回 404）。
func (s *Server) scopedConv(w http.ResponseWriter, r *http.Request) (inbox.Conversation, bool) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return inbox.Conversation{}, false
	}
	c, err := s.Inbox.GetConversation(r.Context(), id)
	if err == nil && !canSee(r, c.IGAccountID) {
		err = inbox.ErrNotFound
	}
	if err != nil {
		s.inboxErr(w, err)
		return c, false
	}
	return c, true
}

func idParam(r *http.Request) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
}

func (s *Server) inboxErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, inbox.ErrNotFound):
		writeErr(w, http.StatusNotFound, "找不到對話")
	case errors.Is(err, inbox.ErrWindowClosed), errors.Is(err, inbox.ErrEmptyText):
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, inbox.ErrCannotSend):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		log.Printf("inbox: %v", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) listConversations(w http.ResponseWriter, r *http.Request) {
	accounts, ok := accountsFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	list, err := s.Inbox.ListConversations(r.Context(), inbox.ListFilter{Status: q.Get("status"), Q: q.Get("q"), Limit: limit, Accounts: accounts})
	if err != nil {
		s.inboxErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) getConversation(w http.ResponseWriter, r *http.Request) {
	c, ok := s.scopedConv(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) patchConversation(w http.ResponseWriter, r *http.Request) {
	c, ok := s.scopedConv(w, r)
	if !ok {
		return
	}
	id := c.ID
	var in struct {
		Handler *string `json:"handler"`
		Status  *string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if in.Handler != nil {
		if err := s.Inbox.SetHandler(r.Context(), id, *in.Handler); err != nil {
			s.patchErr(w, err)
			return
		}
	}
	if in.Status != nil {
		if err := s.Inbox.SetStatus(r.Context(), id, *in.Status); err != nil {
			s.patchErr(w, err)
			return
		}
	}
	s.getConversation(w, r)
}

func (s *Server) patchErr(w http.ResponseWriter, err error) {
	if errors.Is(err, inbox.ErrNotFound) {
		s.inboxErr(w, err)
		return
	}
	writeErr(w, http.StatusBadRequest, err.Error())
}

func (s *Server) readConversation(w http.ResponseWriter, r *http.Request) {
	c, ok := s.scopedConv(w, r)
	if !ok {
		return
	}
	if err := s.Inbox.MarkRead(r.Context(), c.ID); err != nil {
		s.inboxErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	c, ok := s.scopedConv(w, r)
	if !ok {
		return
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	msgs, err := s.Inbox.ListMessages(r.Context(), c.ID, before, limit)
	if err != nil {
		s.inboxErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.scopedConv(w, r)
	if !ok {
		return
	}
	id := c.ID
	var in struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	m, err := s.Inbox.SendAsAdmin(r.Context(), id, in.Text, auth.UserID(r.Context()))
	if err != nil {
		s.inboxErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

// streamPing 是 SSE 心跳間隔；每次心跳重讀登入者，停用、失去收件匣權限、被別處登入踢掉或可見帳號變更立即生效。
var streamPing = 25 * time.Second

// inboxStream 用 SSE 推送新訊息與對話更新；只推登入者看得到的帳號的事件。
func (s *Server) inboxStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	ch, unsub := s.Hub.Subscribe()
	defer unsub()
	_, _ = io.WriteString(w, ": connected\n\n")
	fl.Flush()

	user := currentUser(r)
	ping := time.NewTicker(streamPing)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			u, err := s.Auth.Me(r.Context(), user.ID)
			// 停用、失去收件匣權限、session 被別處登入取代或撤銷 → 斷線
			if err != nil || u.Disabled || !u.Can(auth.PermInbox) || !u.SameSession(user) {
				return
			}
			user = u
			_, _ = io.WriteString(w, ": ping\n\n")
			fl.Flush()
		case e := <-ch:
			if !user.CanSeeAccount(e.AccountID) {
				continue
			}
			b, _ := json.Marshal(e.Data)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, b)
			fl.Flush()
		}
	}
}

// --- IG webhook（公開路徑，靠簽章驗證） ---

func (s *Server) webhookVerify(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	token := s.Creds.Values().IGVerifyToken // 每次讀目前值：設定頁存檔即生效
	if q.Get("hub.mode") == "subscribe" && token != "" && subtle.ConstantTimeCompare([]byte(q.Get("hub.verify_token")), []byte(token)) == 1 {
		_, _ = io.WriteString(w, q.Get("hub.challenge"))
		return
	}
	http.Error(w, "forbidden", http.StatusForbidden)
}

func (s *Server) webhookReceive(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if !ig.VerifySignature(s.Creds.Values().IGAppSecret, body, r.Header.Get("X-Hub-Signature-256")) {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	if err := s.Inbox.EnqueueWebhook(r.Context(), body); err != nil {
		log.Printf("webhook enqueue: %v", err)
		http.Error(w, "retry", http.StatusInternalServerError) // 讓 Meta 重送
		return
	}
	w.WriteHeader(http.StatusOK)
}
