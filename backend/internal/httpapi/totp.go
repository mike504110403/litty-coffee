package httpapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/mike504110403/litty-coffee/backend/internal/audit"
	"github.com/mike504110403/litty-coffee/backend/internal/auth"
)

// Google Authenticator（TOTP）的 HTTP 端（機制見 internal/auth/totp.go）：
//   - POST /api/auth/totp：密碼正確後的第二步，憑待驗證碼 cookie＋{code} 或 {recoveryCode} 換正式 session
//   - POST /api/auth/totp/setup、/enable：登入者自己綁定（首次登入鎖定中也可用）
//
// 操作紀錄一律不記碼、secret、備援碼：/auth/totp 由這裡記（成功＝登入、失敗＝驗證碼錯誤），setup／enable 由 middleware 記且不記 body。

// totpErr 把驗證碼相關錯誤轉成 HTTP 回應；不是這類錯誤回 false。
func totpErr(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, auth.ErrPendingInvalid):
		writeErr(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, auth.ErrTooManyAttempts):
		writeErr(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, auth.ErrBadCode), errors.Is(err, auth.ErrTOTPNoPendingSetup):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, auth.ErrTOTPAlreadyEnabled):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, auth.ErrTOTPUnavailable):
		writeErr(w, http.StatusServiceUnavailable, err.Error())
	default:
		return false
	}
	return true
}

func (s *Server) verifyTOTP(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(auth.PendingCookieName)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, auth.ErrPendingInvalid.Error())
		return
	}
	var in struct {
		Code         string `json:"code"`
		RecoveryCode string `json:"recoveryCode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	res, err := s.Auth.VerifyMFA(r.Context(), c.Value, in.Code, in.RecoveryCode)
	method := "totp"
	if res.Recovery {
		method = "recovery"
	}
	if err != nil {
		if res.User.ID != 0 && (errors.Is(err, auth.ErrBadCode) || errors.Is(err, auth.ErrTooManyAttempts)) {
			status := http.StatusBadRequest
			if errors.Is(err, auth.ErrTooManyAttempts) {
				status = http.StatusTooManyRequests
			}
			s.recordAuth(r, audit.Entry{AdminID: res.User.ID, AdminName: res.User.DisplayName, Action: audit.ActionTOTPFailed,
				Detail: map[string]any{"username": res.Username, "method": method, "reason": err.Error()}, Status: status})
		}
		if errors.Is(err, auth.ErrPendingInvalid) {
			s.Auth.SetPendingCookie(w, "", -1)
		}
		if !totpErr(w, err) {
			log.Printf("totp verify: %v", err)
			writeErr(w, http.StatusInternalServerError, "伺服器錯誤")
		}
		return
	}
	s.recordAuth(r, audit.Entry{AdminID: res.User.ID, AdminName: res.User.DisplayName, Action: audit.ActionLogin,
		Detail: map[string]any{"username": res.Username, "mfa": method}, Status: http.StatusOK, Success: true})
	s.Auth.SetPendingCookie(w, "", -1)
	s.Auth.SetCookie(w, res.Session, 7*24*3600)
	writeJSON(w, http.StatusOK, res.User)
}

func (s *Server) totpSetup(w http.ResponseWriter, r *http.Request) {
	out, err := s.Auth.StartTOTPSetup(r.Context(), auth.UserID(r.Context()))
	if err != nil {
		if !totpErr(w, err) {
			log.Printf("totp setup: %v", err)
			writeErr(w, http.StatusInternalServerError, "伺服器錯誤")
		}
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) totpEnable(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	codes, err := s.Auth.EnableTOTP(r.Context(), auth.UserID(r.Context()), in.Code)
	if err != nil {
		if !totpErr(w, err) {
			log.Printf("totp enable: %v", err)
			writeErr(w, http.StatusInternalServerError, "伺服器錯誤")
		}
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes})
}
