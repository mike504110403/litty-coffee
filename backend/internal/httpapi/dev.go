package httpapi

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mike504110403/litty-coffee/backend/internal/ig"
)

// DevAccountID 是模擬器使用的假 IG 商家帳號 ID。
const DevAccountID = "dev-litty-coffee"

// simulate 假裝某位 IG 用戶傳了一則 DM：組出與真 webhook 同形的 payload，走同一條入庫路徑。
func (s *Server) simulate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Name     string `json:"name"`
		Text     string `json:"text"`
		Image    string `json:"image"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	in.Username = strings.TrimPrefix(strings.TrimSpace(in.Username), "@")
	if in.Username == "" || (strings.TrimSpace(in.Text) == "" && in.Image == "") {
		writeErr(w, http.StatusBadRequest, "username 與 text（或 image）必填")
		return
	}
	sum := sha1.Sum([]byte(in.Username))
	igsid := "dev-" + hex.EncodeToString(sum[:6])
	if s.Fake != nil {
		name := in.Name
		if name == "" {
			name = in.Username
		}
		s.Fake.Register(igsid, ig.Profile{Username: in.Username, Name: name})
	}

	now := time.Now()
	msg := map[string]any{"mid": "dev-in-" + strconv.FormatInt(now.UnixNano(), 36), "text": in.Text}
	if in.Image != "" {
		msg["attachments"] = []map[string]any{{"type": "image", "payload": map[string]string{"url": in.Image}}}
	}
	payload := map[string]any{
		"object": "instagram",
		"entry": []map[string]any{{
			"id":   DevAccountID,
			"time": now.UnixMilli(),
			"messaging": []map[string]any{{
				"sender":    map[string]string{"id": igsid},
				"recipient": map[string]string{"id": DevAccountID},
				"timestamp": now.UnixMilli(),
				"message":   msg,
			}},
		}},
	}
	b, _ := json.Marshal(payload)
	if err := s.Inbox.EnqueueWebhook(r.Context(), b); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"igsid": igsid})
}
