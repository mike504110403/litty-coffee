package monitor

import (
	"fmt"
	"time"
)

// JobsStatus 是「背景工作」燈：webhook 積壓（全系統）＋排程貼文（只算看得到的帳號）。
type JobsStatus struct {
	Light
	Pending      int        `json:"pending"`
	OldestAt     *time.Time `json:"oldestAt"`
	FailedPosts  int        `json:"failedPosts"`
	OverduePosts int        `json:"overduePosts"`
	NextPostAt   *time.Time `json:"nextPostAt"`
}

// View 是給某位登入者看的系統狀態：只含他看得到的帳號；Meta 與排程依這些帳號彙總。
type View struct {
	CheckedAt time.Time       `json:"checkedAt"`
	Meta      Light           `json:"meta"`
	Accounts  []AccountStatus `json:"accounts"`
	Claude    ClaudeStatus    `json:"claude"`
	Jobs      JobsStatus      `json:"jobs"`
	DB        DBStatus        `json:"db"`
}

// View 依可見範圍過濾（安全邊界：看不到的帳號不出現在任何欄位、也不計入彙總）。
func (s Snapshot) View(canSee func(accountID int64) bool) View {
	v := View{CheckedAt: s.CheckedAt, Claude: s.Claude, DB: s.DB, Accounts: []AccountStatus{}}
	var posts PostStats
	for _, a := range s.Accounts {
		if !canSee(a.ID) {
			continue
		}
		v.Accounts = append(v.Accounts, a)
		posts.Failed += a.Posts.Failed
		posts.Overdue += a.Posts.Overdue
		posts.OverdueSecs = max(posts.OverdueSecs, a.Posts.OverdueSecs)
		if a.Posts.NextAt != nil && (posts.NextAt == nil || a.Posts.NextAt.Before(*posts.NextAt)) {
			posts.NextAt = a.Posts.NextAt
		}
	}
	v.Meta = MetaLight(v.Accounts, s.CheckedAt)
	v.Jobs = JobsStatus{
		Light:   worstOf(s.Queue.Light, PostsLight(posts, s.CheckedAt)),
		Pending: s.Queue.Pending, OldestAt: s.Queue.OldestAt,
		FailedPosts: posts.Failed, OverduePosts: posts.Overdue, NextPostAt: posts.NextAt,
	}
	if s.CheckedAt.IsZero() {
		v.Jobs.Light = Light{Yellow, "檢查中", s.CheckedAt}
	}
	return v
}

// MetaLight 彙總啟用中帳號的燈號：全綠＝綠；有紅＝紅；其餘黃。沒有啟用中的帳號＝黃。
func MetaLight(accounts []AccountStatus, now time.Time) Light {
	n, red, yellow := 0, 0, 0
	for _, a := range accounts {
		if !a.Active {
			continue
		}
		n++
		switch a.Status {
		case Red:
			red++
		case Yellow:
			yellow++
		}
	}
	switch {
	case now.IsZero():
		return Light{Yellow, "檢查中", now}
	case n == 0:
		return Light{Yellow, "沒有啟用中的 IG 帳號", now}
	case red > 0:
		return Light{Red, fmt.Sprintf("%d 個帳號異常", red), now}
	case yellow > 0:
		return Light{Yellow, fmt.Sprintf("%d 個帳號需要注意", yellow), now}
	}
	return Light{Green, fmt.Sprintf("%d 個帳號都正常", n), now}
}
