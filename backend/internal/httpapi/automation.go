package httpapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mike504110403/litty-coffee/backend/internal/automation"
	"github.com/mike504110403/litty-coffee/backend/internal/inbox"
)

func (s *Server) automationRoutes(r chi.Router) {
	r.Route("/automation", func(r chi.Router) {
		r.Get("/status", s.autoStatus)
		r.Get("/settings", s.getAutoSettings)
		r.Put("/settings", s.putAutoSettings)

		r.Get("/rules", s.listRules)
		r.Post("/rules", s.createRule)
		r.Put("/rules/{id}", s.updateRule)
		r.Delete("/rules/{id}", s.deleteRule)

		r.Get("/knowledge", s.listKnowledge)
		r.Post("/knowledge", s.saveKnowledge)
		r.Put("/knowledge/{id}", s.saveKnowledge)
		r.Delete("/knowledge/{id}", s.deleteKnowledge)

		r.Post("/test", s.testAutomation)
	})
}

func (s *Server) autoErr(w http.ResponseWriter, err error) {
	if errors.Is(err, automation.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "找不到資料")
		return
	}
	log.Printf("automation: %v", err)
	writeErr(w, http.StatusInternalServerError, "伺服器錯誤")
}

func (s *Server) autoStatus(w http.ResponseWriter, r *http.Request) {
	model := s.AIModel
	if s.CaptionAI != nil && s.CaptionAI.Enabled() {
		model = s.CaptionAI.Model()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"aiConfigured": s.Auto.AIEnabled(),
		"model":        model,
	})
}

func (s *Server) getAutoSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.Auto.Store.GetSettings(r.Context())
	if err != nil {
		s.autoErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) putAutoSettings(w http.ResponseWriter, r *http.Request) {
	var st automation.Settings
	if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	st, err := s.Auto.Store.SaveSettings(r.Context(), st)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) listRules(w http.ResponseWriter, r *http.Request) {
	list, err := s.Auto.Store.ListRules(r.Context(), false)
	if err != nil {
		s.autoErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func decodeRule(r *http.Request) (automation.Rule, error) {
	var in automation.Rule
	err := json.NewDecoder(r.Body).Decode(&in)
	return in, err
}

func (s *Server) createRule(w http.ResponseWriter, r *http.Request) {
	in, err := decodeRule(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	out, err := s.Auto.Store.CreateRule(r.Context(), in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) updateRule(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	in, err := decodeRule(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	in.ID = id
	out, err := s.Auto.Store.UpdateRule(r.Context(), in)
	if errors.Is(err, automation.ErrNotFound) {
		s.autoErr(w, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteRule(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := s.Auto.Store.DeleteRule(r.Context(), id); err != nil {
		s.autoErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listKnowledge(w http.ResponseWriter, r *http.Request) {
	list, err := s.Auto.Store.ListKnowledge(r.Context())
	if err != nil {
		s.autoErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) saveKnowledge(w http.ResponseWriter, r *http.Request) {
	var in automation.Knowledge
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	in.ID = 0
	if chi.URLParam(r, "id") != "" {
		id, err := idParam(r)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad id")
			return
		}
		in.ID = id
	}
	out, err := s.Auto.Store.SaveKnowledge(r.Context(), in)
	if errors.Is(err, automation.ErrNotFound) {
		s.autoErr(w, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteKnowledge(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := s.Auto.Store.DeleteKnowledge(r.Context(), id); err != nil {
		s.autoErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testAutomation 是試答沙盒：用目前的規則、知識庫、設定跑一次，不送出。
func (s *Server) testAutomation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		History []struct {
			FromCustomer bool   `json:"fromCustomer"`
			Text         string `json:"text"`
		} `json:"history"`
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Text) == "" {
		writeErr(w, http.StatusBadRequest, "請輸入測試訊息")
		return
	}
	st, err := s.Auto.Store.GetSettings(r.Context())
	if err != nil {
		s.autoErr(w, err)
		return
	}
	st.AIEnabled = true // 沙盒一律試 AI，方便還沒開啟前先調
	var hist []inbox.Message
	for _, h := range in.History {
		dir := "out"
		if h.FromCustomer {
			dir = "in"
		}
		hist = append(hist, inbox.Message{Direction: dir, Kind: "text", Text: h.Text})
	}
	hist = append(hist, inbox.Message{Direction: "in", Kind: "text", Text: in.Text})

	start := time.Now()
	out, err := s.Auto.Evaluate(r.Context(), st, hist)
	if err != nil {
		log.Printf("automation test: %v", err)
		writeErr(w, http.StatusBadGateway, "AI 呼叫失敗："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"outcome": out, "elapsedMs": time.Since(start).Milliseconds()})
}
