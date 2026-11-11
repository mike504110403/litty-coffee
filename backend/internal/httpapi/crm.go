package httpapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mike504110403/litty-coffee/backend/internal/crm"
)

func (s *Server) crmRoutes(r chi.Router) {
	r.Get("/contacts", s.listContacts)
	r.Get("/contacts/{id}", s.getContact)
	r.Patch("/contacts/{id}", s.patchContact)

	r.Get("/tags", s.listTags)
	r.Post("/tags", s.createTag)
	r.Put("/tags/{id}", s.updateTag)
	r.Delete("/tags/{id}", s.deleteTag)
}

func (s *Server) analyticsRoutes(r chi.Router) {
	r.Get("/analytics/summary", s.analyticsSummary)
}

func (s *Server) crmErr(w http.ResponseWriter, err error) {
	if errors.Is(err, crm.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "找不到資料")
		return
	}
	log.Printf("crm: %v", err)
	writeErr(w, http.StatusInternalServerError, "伺服器錯誤")
}

func (s *Server) listContacts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tagID, _ := strconv.ParseInt(q.Get("tag"), 10, 64)
	list, err := s.CRM.List(r.Context(), crm.ListFilter{Q: q.Get("q"), TagID: tagID, Sort: q.Get("sort")})
	if err != nil {
		s.crmErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) getContact(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	d, err := s.CRM.Get(r.Context(), id)
	if err != nil {
		s.crmErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) patchContact(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	var in struct {
		Note   *string `json:"note"`
		TagIDs []int64 `json:"tagIds"` // 省略＝不動；[]＝清空
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.CRM.Update(r.Context(), id, in.Note, in.TagIDs); err != nil {
		s.crmErr(w, err)
		return
	}
	s.getContact(w, r)
}

func (s *Server) listTags(w http.ResponseWriter, r *http.Request) {
	list, err := s.CRM.Tags(r.Context())
	if err != nil {
		s.crmErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type tagIn struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

func (s *Server) createTag(w http.ResponseWriter, r *http.Request) {
	var in tagIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	t, err := s.CRM.CreateTag(r.Context(), in.Name, in.Color)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) updateTag(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	var in tagIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	t, err := s.CRM.UpdateTag(r.Context(), id, in.Name, in.Color)
	if errors.Is(err, crm.ErrNotFound) {
		s.crmErr(w, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) deleteTag(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := s.CRM.DeleteTag(r.Context(), id); err != nil {
		s.crmErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) analyticsSummary(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 365 {
		days = 7
	}
	sum, err := s.Analytics.Summary(r.Context(), days, time.Now())
	if err != nil {
		log.Printf("analytics: %v", err)
		writeErr(w, http.StatusInternalServerError, "統計失敗")
		return
	}
	writeJSON(w, http.StatusOK, sum)
}
