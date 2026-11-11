package httpapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mike504110403/litty-coffee/backend/internal/auth"
)

// adminRoutes：管理員帳號管理（需 admins 權限；細部規則在 auth.Service）。
func (s *Server) adminRoutes(r chi.Router) {
	r.Get("/admins", s.listAdmins)
	r.Post("/admins", s.createAdmin)
	r.Patch("/admins/{id}", s.updateAdmin)
	r.Post("/admins/{id}/password", s.resetAdminPassword)
	r.Post("/admins/{id}/totp/reset", s.resetAdminTOTP) // 超管專屬（auth.ResetTOTP 把關）
}

func (s *Server) adminErr(w http.ResponseWriter, err error) {
	var pe *auth.PolicyError
	var ie *auth.InputError
	switch {
	case errors.As(err, &pe):
		writeErr(w, http.StatusForbidden, pe.Msg)
	case errors.As(err, &ie):
		writeErr(w, http.StatusBadRequest, ie.Msg)
	case errors.Is(err, auth.ErrUserNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, auth.ErrUsernameTaken):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		log.Printf("admins: %v", err)
		writeErr(w, http.StatusInternalServerError, "伺服器錯誤")
	}
}

func (s *Server) listAdmins(w http.ResponseWriter, r *http.Request) {
	list, err := s.Auth.ListAdmins(r.Context())
	if err != nil {
		s.adminErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"admins": list, "permissions": auth.AllPerms})
}

func (s *Server) resetAdminTOTP(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	actor, _ := auth.CurrentUser(r.Context())
	if err := s.Auth.ResetTOTP(r.Context(), actor, id); err != nil {
		s.adminErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createAdmin(w http.ResponseWriter, r *http.Request) {
	var in auth.AdminInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	actor, _ := auth.CurrentUser(r.Context())
	u, err := s.Auth.CreateAdmin(r.Context(), actor, in)
	if err != nil {
		s.adminErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) updateAdmin(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	var in auth.AdminPatch
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	actor, _ := auth.CurrentUser(r.Context())
	u, err := s.Auth.UpdateAdmin(r.Context(), actor, id, in)
	if err != nil {
		s.adminErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) resetAdminPassword(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	actor, _ := auth.CurrentUser(r.Context())
	if err := s.Auth.ResetPassword(r.Context(), actor, id, in.Password); err != nil {
		s.adminErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
