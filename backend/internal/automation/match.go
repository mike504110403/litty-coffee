package automation

import (
	"regexp"
	"strings"

	"golang.org/x/text/width"
)

// normalize 讓比對不受大小寫、全形半形、前後空白影響。
func normalize(s string) string {
	return strings.ToLower(strings.TrimSpace(width.Fold.String(s)))
}

// MatchRule 回傳第一條命中的規則（rules 需已依 priority 排序）。
func MatchRule(rules []Rule, text string) (Rule, bool) {
	t := normalize(text)
	if t == "" {
		return Rule{}, false
	}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		for _, kw := range r.Keywords {
			if matchOne(r.MatchType, kw, text, t) {
				return r, true
			}
		}
	}
	return Rule{}, false
}

func matchOne(kind, kw, raw, norm string) bool {
	switch kind {
	case "exact":
		return norm == normalize(kw)
	case "contains":
		k := normalize(kw)
		return k != "" && strings.Contains(norm, k)
	case "regex":
		re, err := regexp.Compile("(?i)" + kw)
		return err == nil && re.MatchString(raw)
	}
	return false
}
