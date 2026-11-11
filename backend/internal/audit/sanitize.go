package audit

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"unicode"
)

// 機密遮蔽規則：鍵名（轉小寫、去掉非英數字後）含下列任一字即視為機密，非空值一律換成 Redacted。
// 涵蓋 password／currentPassword、token／igVerifyToken／accessToken、secret／igAppSecret、apiKey／anthropicApiKey、
// 以及任何以 key 結尾的鍵（anthropicKey 之類）。整個 body 都是密碼的路由（改密碼、重設密碼）在 Routes 標 NoBody，不記 body。
var secretWords = []string{"password", "passwd", "pwd", "token", "secret", "apikey", "session", "cookie", "authorization", "credential"}

// Redacted 是遮蔽後的值。空字串／null 保留原樣（看得出「清空」但不洩漏內容）。
const Redacted = "[已遮蔽]"

// MaxText 是 detail 內單一字串保留的字數（超過截斷加「…」）。
const MaxText = 200

const maxItems = 50 // 陣列最多保留幾項

// IsSecretKey 回報鍵名是否屬於機密。
func IsSecretKey(k string) bool {
	var b strings.Builder
	for _, r := range strings.ToLower(k) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	n := b.String()
	if n == "" {
		return false
	}
	for _, w := range secretWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	return strings.HasSuffix(n, "key")
}

// Truncate 把字串截到 MaxText 個字。
func Truncate(s string) string {
	r := []rune(s)
	if len(r) <= MaxText {
		return s
	}
	return string(r[:MaxText]) + "…"
}

// SanitizeJSON 把 request body 轉成可存進 detail 的摘要：遮蔽機密鍵、截斷長字串與長陣列。
// 不是 JSON 時回截斷後的原文（仍是字串，無結構可遮蔽，所以只在呼叫端確認不是密碼類路由時使用）。
func SanitizeJSON(body []byte) any {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return Truncate(string(body))
	}
	return Sanitize(v)
}

// Sanitize 遞迴遮蔽與截斷（輸入為 encoding/json 解出的型別）。
func Sanitize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if IsSecretKey(k) {
				if val == nil || val == "" {
					out[k] = val
				} else {
					out[k] = Redacted
				}
				continue
			}
			out[k] = Sanitize(val)
		}
		return out
	case []any:
		n := len(t)
		if n > maxItems {
			n = maxItems
		}
		out := make([]any, 0, n+1)
		for _, x := range t[:n] {
			out = append(out, Sanitize(x))
		}
		if len(t) > maxItems {
			out = append(out, "…")
		}
		return out
	case string:
		return Truncate(t)
	default:
		return v
	}
}

// ClientIP 取請求來源 IP：有 X-Forwarded-For 取第一個（部署在反向代理後面時），否則 RemoteAddr。
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ip := strings.TrimSpace(strings.Split(xff, ",")[0])
		if ip != "" {
			return truncateN(ip, 64)
		}
	}
	if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
		return truncateN(ip, 64)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// UserAgent 取截斷過的 User-Agent。
func UserAgent(r *http.Request) string { return truncateN(r.UserAgent(), 300) }

func truncateN(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
