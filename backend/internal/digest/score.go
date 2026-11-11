package digest

import "fmt"

// Factors 是一段對話「需要處理」的訊號（每日摘要的待處理清單用）。
type Factors struct {
	Unanswered  bool   `json:"unanswered"`  // 對話開著、最後一則是顧客訊息（還沒人回）
	HandedOff   bool   `json:"handedOff"`   // 已轉真人，但顧客最後一則之後還沒有真人回覆
	Complaint   bool   `json:"complaint"`   // 當天顧客訊息有客訴意圖
	Reservation bool   `json:"reservation"` // 當天顧客訊息有訂位意圖
	Risk        string `json:"risk"`        // 當天有傳訊的問題顧客（watch）／黑名單（blocked）
	OpenReviews int    `json:"openReviews"` // 未處理的待改進問答
	WaitMinutes int    `json:"waitMinutes"` // 顧客最後一則訊息到產生摘要時已等幾分鐘
}

// 分數＝類別分 × tierUnit ＋ 等待分鐘（上限 maxWaitMinutes）。
// 類別權重是 2 的次方：較高的一類永遠勝過所有較低類別的總和，所以排序依序是
// 未回覆 ＞ 轉真人未回 ＞ 客訴／訂位 ＞ 問題顧客／黑名單 ＞ 待改進；同類別組合時等越久越前面。
const (
	weightUnanswered = 16
	weightHandedOff  = 8
	weightIntent     = 4 // 客訴或訂位（同一級，有其一即算）
	weightRisk       = 2
	weightReview     = 1

	tierUnit       = 100000
	maxWaitMinutes = tierUnit - 1
)

// Score 回這段對話的重要度；0＝不需要列入待處理。
func Score(f Factors) int64 {
	tier := 0
	if f.Unanswered {
		tier += weightUnanswered
	}
	if f.HandedOff {
		tier += weightHandedOff
	}
	if f.Complaint || f.Reservation {
		tier += weightIntent
	}
	if f.Risk == "watch" || f.Risk == "blocked" {
		tier += weightRisk
	}
	if f.OpenReviews > 0 {
		tier += weightReview
	}
	if tier == 0 {
		return 0
	}
	w := min(max(f.WaitMinutes, 0), maxWaitMinutes)
	return int64(tier)*tierUnit + int64(w)
}

// Reasons 回待處理原因的中文標籤（依重要度）。
func Reasons(f Factors) []string {
	out := []string{}
	if f.Unanswered {
		out = append(out, "未回覆 "+waitLabel(f.WaitMinutes))
	}
	if f.HandedOff {
		out = append(out, "轉真人未回")
	}
	if f.Complaint {
		out = append(out, "客訴")
	}
	if f.Reservation {
		out = append(out, "訂位")
	}
	switch f.Risk {
	case "watch":
		out = append(out, "問題顧客")
	case "blocked":
		out = append(out, "黑名單")
	}
	if f.OpenReviews > 0 {
		out = append(out, fmt.Sprintf("待改進 %d 則", f.OpenReviews))
	}
	return out
}

func waitLabel(min int) string {
	switch {
	case min < 60:
		return fmt.Sprintf("%d 分鐘", max(min, 0))
	case min < 48*60:
		return fmt.Sprintf("%d 小時", min/60)
	default:
		return fmt.Sprintf("%d 天", min/(24*60))
	}
}
