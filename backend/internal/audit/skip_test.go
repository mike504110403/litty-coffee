package audit

import "testing"

func TestSkippedRoutes(t *testing.T) {
	if !Skipped("POST", "/conversations/{id}/read") {
		t.Fatal("標記已讀不應寫操作紀錄")
	}
	if Skipped("POST", "/conversations/{id}/messages") {
		t.Fatal("回覆私訊必須寫操作紀錄")
	}
	for k := range skipped {
		found := false
		for _, r := range Routes {
			if r.Method+" "+r.Pattern == k {
				found = true
			}
		}
		if !found {
			t.Fatalf("skipped 裡的 %s 不在 Routes（打錯字或路由已改名）", k)
		}
	}
}
