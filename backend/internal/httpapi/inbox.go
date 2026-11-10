package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

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
	r.Get("/inbox/stream", s.inboxStream)
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
	default:
		log.Printf("inbox: %v", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) listConversations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	list, err := s.Inbox.ListConversations(r.Context(), inbox.ListFilter{Status: q.Get("status"), Q: q.Get("q"), Limit: limit})
	if err != nil {
		s.inboxErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) getConversation(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	c, err := s.Inbox.GetConversation(r.Context(), id)
	if err != nil {
		s.inboxErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) patchConversation(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
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
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := s.Inbox.MarkRead(r.Context(), id); err != nil {
		s.inboxErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	msgs, err := s.Inbox.ListMessages(r.Context(), id, before, limit)
	if err != nil {
		s.inboxErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	m, err := s.Inbox.Send(r.Context(), id, in.Text, "human")
	if err != nil {
		s.inboxErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

// inboxStream 用 SSE 推送新訊息與對話更新。
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

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			_, _ = io.WriteString(w, ": ping\n\n")
			fl.Flush()
		case e := <-ch:
			b, _ := json.Marshal(e.Data)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, b)
			fl.Flush()
		}
	}
}

// --- IG webhook（公開路徑，靠簽章驗證） ---

func (s *Server) webhookVerify(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("hub.mode") == "subscribe" && s.IGVerifyToken != "" && q.Get("hub.verify_token") == s.IGVerifyToken {
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
	if !ig.VerifySignature(s.IGAppSecret, body, r.Header.Get("X-Hub-Signature-256")) {
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
