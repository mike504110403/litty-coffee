package digest

import (
	"fmt"
	"strings"

	"github.com/mike504110403/litty-coffee/backend/internal/automation"
)

// intentNames 是意圖的中文名（與前端 intentLabels 一致）。
var intentNames = map[string]string{
	"hours": "營業資訊", "menu": "菜單價格", "reservation": "訂位", "location": "交通位置", "promotion": "活動優惠",
	"complaint": "客訴", "business": "合作洽談", "chitchat": "閒聊", "other": "其他",
}

// BuildAIData 把摘要整理成送給模型的資料：只有彙總數字、熱門意圖，以及前 maxTodoForAI 則待處理的原因與
// 顧客最後一則訊息的短節錄（≤60 字、已壓成一行）。不含顧客名稱、帳號、完整對話。總長上限 automation.DigestDataMaxRunes。
func BuildAIData(c Content) string {
	var b strings.Builder
	t, p := c.Totals, c.Previous
	fmt.Fprintf(&b, "日期：%s\n", c.Date)
	fmt.Fprintf(&b, "顧客訊息 %d（前一天 %d）｜互動顧客 %d（前一天 %d）｜新顧客 %d｜自動回覆 %d｜真人回覆 %d｜需真人的對話 %d\n",
		t.Inbound, p.Inbound, t.ActiveContacts, p.ActiveContacts, t.NewContacts, t.OutboundAuto, t.OutboundHuman, t.HumanConvs)
	if c.AutoShare != nil {
		fmt.Fprintf(&b, "自動回覆占比 %.0f%%", *c.AutoShare*100)
		if c.PrevAutoShare != nil {
			fmt.Fprintf(&b, "（前一天 %.0f%%）", *c.PrevAutoShare*100)
		}
		b.WriteString("\n")
	}
	if c.MedianSecs != nil {
		fmt.Fprintf(&b, "回應時間中位數 %s\n", durationLabel(*c.MedianSecs))
	}
	if len(c.Intents) > 0 {
		parts := make([]string, 0, len(c.Intents))
		for _, it := range c.Intents {
			name := intentNames[it.Key]
			if name == "" {
				name = "其他"
			}
			parts = append(parts, fmt.Sprintf("%s %d", name, it.Count))
		}
		fmt.Fprintf(&b, "熱門意圖：%s\n", strings.Join(parts, "、"))
	}
	if c.TodoTotal == 0 {
		b.WriteString("待處理：無\n")
	} else {
		n := min(len(c.Todo), maxTodoForAI)
		fmt.Fprintf(&b, "待處理（依重要度，共 %d 則，列前 %d 則）：\n", c.TodoTotal, n)
		for i, it := range c.Todo[:n] {
			fmt.Fprintf(&b, "#%d %s", i+1, strings.Join(it.Reasons, "、"))
			if it.LastText != "" {
				fmt.Fprintf(&b, "｜「%s」", strings.NewReplacer("「", "", "」", "").Replace(it.LastText))
			}
			b.WriteString("\n")
		}
	}
	out := b.String()
	if r := []rune(out); len(r) > automation.DigestDataMaxRunes {
		out = string(r[:automation.DigestDataMaxRunes])
	}
	return out
}

func durationLabel(secs float64) string {
	switch {
	case secs < 60:
		return fmt.Sprintf("%.0f 秒", secs)
	case secs < 3600:
		return fmt.Sprintf("%.0f 分鐘", secs/60)
	default:
		return fmt.Sprintf("%.1f 小時", secs/3600)
	}
}
