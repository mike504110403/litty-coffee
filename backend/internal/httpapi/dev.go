package httpapi

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mike504110403/litty-coffee/backend/internal/ig"
)

// ErrSimNotConnected 是還沒（模擬）連結任何帳號時的提示。
const errSimNotConnected = "請先到設定頁連結 IG 帳號（模擬）"

// simAccount 決定模擬訊息要送到哪個我方帳號：指定的（須可見、已連結、啟用）或第一個可見且已連結的。
func (s *Server) simAccount(w http.ResponseWriter, r *http.Request, want int64) (igUserID string, id int64, ok bool) {
	list, err := s.visibleAccounts(r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return "", 0, false
	}
	usable := func(connected, active bool) bool { return connected && active }
	if want > 0 {
		if !canSee(r, want) {
			writeErr(w, http.StatusForbidden, errAccountForbidden)
			return "", 0, false
		}
		for _, a := range list {
			if a.ID == want {
				if !usable(a.Connected, a.Active) {
					writeErr(w, http.StatusConflict, "這個帳號未連結或已停用")
					return "", 0, false
				}
				return a.IGUserID, a.ID, true
			}
		}
		writeErr(w, http.StatusNotFound, "找不到這個 IG 帳號")
		return "", 0, false
	}
	for _, a := range list {
		if usable(a.Connected, a.Active) {
			return a.IGUserID, a.ID, true
		}
	}
	writeErr(w, http.StatusConflict, errSimNotConnected)
	return "", 0, false
}

// simulate 假裝某位 IG 用戶傳了一則 DM 給我方某個帳號：組出與真 webhook 同形的 payload，走同一條入庫路徑。
func (s *Server) simulate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Account  int64  `json:"account"` // 收訊的我方帳號（ig_accounts.id）；0＝第一個已連結的
		Username string `json:"username"`
		Name     string `json:"name"`
		Text     string `json:"text"`
		Image    string `json:"image"`
		Action   string `json:"action"` // message（預設）| read | react | unsend
		Emoji    string `json:"emoji"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	in.Username = strings.TrimPrefix(strings.TrimSpace(in.Username), "@")
	if in.Action == "" {
		in.Action = "message"
	}
	if in.Username == "" || (in.Action == "message" && strings.TrimSpace(in.Text) == "" && in.Image == "") {
		writeErr(w, http.StatusBadRequest, "username 與 text（或 image）必填")
		return
	}
	accountIGID, accountID, ok := s.simAccount(w, r, in.Account)
	if !ok {
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
	event := map[string]any{
		"sender":    map[string]string{"id": igsid},
		"recipient": map[string]string{"id": accountIGID},
		"timestamp": now.UnixMilli(),
	}
	// 找這位客人對話裡的最後一則（out＝我方送出、in＝客人傳的）
	lastMid := func(direction string) (string, error) {
		var mid string
		err := s.DB.QueryRow(r.Context(), `
SELECT m.ig_mid FROM messages m JOIN conversations c ON c.id = m.conversation_id JOIN contacts ct ON ct.id = c.contact_id
WHERE ct.igsid = $1 AND m.direction = $2 AND m.deleted_at IS NULL AND m.ig_mid IS NOT NULL AND ct.ig_account_id = $3
ORDER BY m.created_at DESC, m.id DESC LIMIT 1`, igsid, direction, accountID).Scan(&mid)
		if err != nil {
			return "", fmt.Errorf("找不到可操作的訊息")
		}
		return mid, nil
	}
	switch in.Action {
	case "read":
		event["read"] = map[string]string{"mid": "dev-read"}
	case "react", "unsend":
		dir := "out"
		if in.Action == "unsend" {
			dir = "in"
		}
		mid, err := lastMid(dir)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if in.Action == "unsend" {
			event["message"] = map[string]any{"mid": mid, "is_deleted": true}
		} else {
			action, emoji := "react", in.Emoji
			if emoji == "" {
				action = "unreact"
			}
			event["reaction"] = map[string]string{"mid": mid, "action": action, "reaction": "other", "emoji": emoji}
		}
	default:
		msg := map[string]any{"mid": "dev-in-" + strconv.FormatInt(now.UnixNano(), 36), "text": in.Text}
		if in.Image != "" {
			msg["attachments"] = []map[string]any{{"type": "image", "payload": map[string]string{"url": in.Image}}}
		}
		event["message"] = msg
	}
	payload := map[string]any{
		"object": "instagram",
		"entry": []map[string]any{{
			"id":        accountIGID,
			"time":      now.UnixMilli(),
			"messaging": []map[string]any{event},
		}},
	}
	b, _ := json.Marshal(payload)
	if err := s.Inbox.EnqueueWebhook(r.Context(), b); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"igsid": igsid, "account": accountID})
}
