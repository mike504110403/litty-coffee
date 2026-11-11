package automation

import (
	_ "embed"
	"strings"
	"unicode"
)

// SecurityRules 是所有 Claude 呼叫共用的防提示注入規則（prompts/security.md）。
// 每個系統提示都必須經 WithGuard 組出，讓規則固定排在最前面；新增 AI 呼叫時一律照做（guard_test 會檢查）。
//
//go:embed prompts/security.md
var SecurityRules string

// WithGuard 把共用安全規則放在系統提示最前面。
func WithGuard(system string) string {
	return strings.TrimSpace(SecurityRules) + "\n\n" + strings.TrimSpace(system)
}

// Untrusted 清理一段不可信文字（顧客訊息、節錄、外部資料），讓它無法偽造區塊邊界或夾帶隱形控制字元：
//   - 去掉零寬字元、雙向文字控制字元與其他控制字元（保留換行與 tab）
//   - 角括號換成全形，無法寫出 <conversation>、</data> 之類的標籤
//   - 統一換行；超過 maxRunes（>0）時截斷並加「…」
func Untrusted(s string, maxRunes int) string {
	s = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == '<':
			b.WriteRune('＜')
		case r == '>':
			b.WriteRune('＞')
		case isInvisible(r):
			// 丟掉
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if maxRunes > 0 {
		if rs := []rune(out); len(rs) > maxRunes {
			out = string(rs[:maxRunes]) + "…"
		}
	}
	return out
}

// isInvisible：控制字元、零寬字元、雙向文字覆寫／隔離字元、BOM。
func isInvisible(r rune) bool {
	switch {
	case r >= 0x200B && r <= 0x200F, // 零寬空白／連接／非連接、LRM、RLM
		r >= 0x202A && r <= 0x202E, // 雙向嵌入／覆寫
		r >= 0x2060 && r <= 0x2064, // word joiner、隱形運算子
		r >= 0x2066 && r <= 0x2069, // 雙向隔離
		r == 0xFEFF, r == 0x00AD:   // BOM、軟連字號
		return true
	}
	return unicode.IsControl(r)
}
