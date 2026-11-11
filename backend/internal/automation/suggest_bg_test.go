package automation

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mike504110403/litty-coffee/backend/internal/inbox"
	"github.com/mike504110403/litty-coffee/backend/internal/realtime"
)

// bgSuggester 是背景建議用的假 AI：依序回 resps（用完就重複最後一個），可擋住（block）觀察 pending 與並行數。
type bgSuggester struct {
	mu       sync.Mutex
	disabled bool
	resps    []Suggestion
	err      error
	block    chan struct{}
	calls    int
	running  int
	maxRun   int
	turns    []Turn
}

func (f *bgSuggester) Enabled() bool { return !f.disabled }
func (f *bgSuggester) Model() string { return "fake-model" }
func (f *bgSuggester) Suggest(ctx context.Context, _ Settings, _ []Knowledge, turns []Turn) (Suggestion, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.running++
	f.maxRun = max(f.maxRun, f.running)
	f.turns = turns
	block := f.block
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running--
	if f.err != nil {
		return Suggestion{}, f.err
	}
	if len(f.resps) == 0 {
		return okSuggestion(), nil
	}
	return f.resps[min(n, len(f.resps))-1], nil
}

func (f *bgSuggester) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func okSuggestion() Suggestion {
	return Suggestion{Warning: "知識庫沒有價格", Options: []SuggestOption{{Label: "直接回答", Text: "您好，我們 10 點開～"}, {Label: "簡短版", Text: "10 點開喔"}}}
}

// setupBG：setup ＋ 掛上背景建議 worker（假 AI）。
func setupBG(t *testing.T) (*Engine, *fakeAI, *bgSuggester, int64) {
	e, ai, acc := setup(t)
	fs := &bgSuggester{}
	e.Suggest = &SuggestWorker{Store: e.Store, Inbox: e.Inbox, AI: fs, Ctx: e.Ctx, Calls: &CallStats{}}
	return e, ai, fs, acc
}

// inbound 顧客傳一則並走正式的 OnInbound 路徑，等背景處理（自動回覆＋建議）都結束。
func inbound(t *testing.T, e *Engine, igsid, mid, text string) (int64, inbox.Message) {
	t.Helper()
	conv, m := customerSays(t, e, igsid, mid, text)
	e.OnInbound(conv, m)
	settle(e)
	return conv, m
}

func settle(e *Engine) {
	e.bg.Wait()
	e.Suggest.Wait()
}

func current(t *testing.T, e *Engine, conv int64) SuggestionView {
	t.Helper()
	v, err := e.Store.CurrentSuggestion(context.Background(), e.Inbox, conv)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func suggestionEvents(evs []realtime.Event) []SuggestionReadyEvent {
	var out []SuggestionReadyEvent
	for _, ev := range evs {
		if ev.Type == "suggestion.ready" {
			out = append(out, ev.Data.(SuggestionReadyEvent))
		}
	}
	return out
}

func setAutoReply(t *testing.T, e *Engine, acc int64, f func(*Settings)) {
	t.Helper()
	st, err := e.Store.GetSettings(context.Background(), acc)
	if err != nil {
		t.Fatal(err)
	}
	f(&st)
	if _, err := e.Store.SaveSettings(context.Background(), acc, st); err != nil {
		t.Fatal(err)
	}
}

// 真人接手中：顧客訊息進來 → 背景產生、存表、推 suggestion.ready（帶帳號），GET 拿得到。
func TestBgSuggestWhenHumanHandles(t *testing.T) {
	e, ai, fs, acc := setupBG(t)
	ctx := context.Background()
	conv, _ := customerSays(t, e, "u1", "m0", "嗨")
	if err := e.Inbox.SetHandler(ctx, conv, "human"); err != nil {
		t.Fatal(err)
	}
	events, unsub := e.Inbox.Hub.Subscribe()
	defer unsub()
	_, m := inbound(t, e, "u1", "m1", "你們幾點開？")
	if fs.count() != 1 || ai.calls != 0 {
		t.Fatalf("suggest calls %d, reply calls %d", fs.count(), ai.calls)
	}
	v := current(t, e, conv)
	if v.Status != SuggestionReady || v.ForMessageID != m.ID || len(v.Options) != 2 || v.Options[0].Basis == nil ||
		v.Warning != "知識庫沒有價格" || v.CreatedAt == nil || v.Error != "" {
		t.Fatalf("view: %+v", v)
	}
	evs := suggestionEvents(eventsOf(events))
	if len(evs) != 1 || evs[0] != (SuggestionReadyEvent{ConversationID: conv, IGAccountID: acc, ForMessageID: m.ID}) {
		t.Fatalf("events: %+v", evs)
	}
	for _, ev := range eventsOf(events) {
		if ev.Type == "suggestion.ready" && ev.AccountID != acc {
			t.Fatalf("事件必須帶帳號（SSE 依此過濾）：%+v", ev)
		}
	}
	if last, _ := e.Suggest.Calls.Last(); last == nil || last.Kind != "suggest" || !last.OK {
		t.Fatalf("應計入 Calls：%+v", last)
	}
	var model string
	if err := e.Store.DB.QueryRow(ctx, `SELECT model FROM conversation_suggestions WHERE conversation_id = $1`, conv).Scan(&model); err != nil || model != "fake-model" {
		t.Fatalf("model: %q %v", model, err)
	}
}

// 自動回覆關閉、AI 轉真人之後都會產生；AI 正常回覆時不會。
func TestBgSuggestTriggers(t *testing.T) {
	t.Run("auto reply off", func(t *testing.T) {
		e, ai, fs, acc := setupBG(t)
		setAutoReply(t, e, acc, func(s *Settings) { s.AutoReplyEnabled = false })
		conv, m := inbound(t, e, "u1", "m1", "在嗎")
		if fs.count() != 1 || ai.calls != 0 || current(t, e, conv).Status != SuggestionReady || current(t, e, conv).ForMessageID != m.ID {
			t.Fatalf("calls %d/%d view %+v", fs.count(), ai.calls, current(t, e, conv))
		}
	})
	t.Run("after AI handoff", func(t *testing.T) {
		e, ai, fs, _ := setupBG(t)
		ai.d = AIDecision{Action: "handoff", Intent: "reservation", Reason: "要確認訂位"}
		conv, m := inbound(t, e, "u1", "m1", "幫我訂明天兩位")
		if last := lastOut(t, e, conv); last.Text != "請稍等店員" {
			t.Fatalf("應先送轉真人訊息：%+v", last)
		}
		if fs.count() != 1 || current(t, e, conv).Status != SuggestionReady || current(t, e, conv).ForMessageID != m.ID {
			t.Fatalf("calls %d view %+v", fs.count(), current(t, e, conv))
		}
	})
	t.Run("rule handoff", func(t *testing.T) {
		e, _, fs, acc := setupBG(t)
		if _, err := e.Store.CreateRule(context.Background(), acc, Rule{Name: "客訴", MatchType: "contains", Keywords: []string{"退錢"},
			Reply: "抱歉，店員馬上處理", Handoff: true, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		conv, _ := inbound(t, e, "u1", "m1", "我要退錢")
		if fs.count() != 1 || current(t, e, conv).Status != SuggestionReady {
			t.Fatalf("calls %d view %+v", fs.count(), current(t, e, conv))
		}
	})
	t.Run("bot replies normally", func(t *testing.T) {
		e, ai, fs, _ := setupBG(t)
		ai.d = AIDecision{Action: "reply", Reply: "10 點開喔", Intent: "hours"}
		conv, _ := inbound(t, e, "u1", "m1", "幾點開")
		if lastOut(t, e, conv).Text != "10 點開喔" {
			t.Fatal("AI 應已回覆")
		}
		if fs.count() != 0 || current(t, e, conv).Status != SuggestionNone {
			t.Fatalf("bot 回覆時不該產生：calls %d view %+v", fs.count(), current(t, e, conv))
		}
	})
	t.Run("blocked contact", func(t *testing.T) {
		e, _, fs, acc := setupBG(t)
		setAutoReply(t, e, acc, func(s *Settings) { s.AutoReplyEnabled = false })
		conv, _ := customerSays(t, e, "u1", "m0", "嗨")
		if _, err := e.Store.DB.Exec(context.Background(), `UPDATE contacts SET risk = 'blocked' WHERE igsid = 'u1'`); err != nil {
			t.Fatal(err)
		}
		inbound(t, e, "u1", "m1", "騷擾")
		if fs.count() != 0 || current(t, e, conv).Status != SuggestionNone {
			t.Fatalf("黑名單不該產生：calls %d", fs.count())
		}
		e.Suggest.Schedule(conv, current(t, e, conv).ForMessageID) // 直接排程也擋
		settle(e)
		if fs.count() != 0 {
			t.Fatal("黑名單不該產生")
		}
	})
}

// 合併秒數內連傳：只針對最後一則產生一次；同一則再排程也不會重產生。
func TestBgSuggestDebounceOnce(t *testing.T) {
	e, _, fs, acc := setupBG(t)
	setAutoReply(t, e, acc, func(s *Settings) { s.AutoReplyEnabled = false; s.DebounceSecs = 1 })
	conv, m1 := customerSays(t, e, "u1", "m1", "請問")
	e.OnInbound(conv, m1)
	_, m2 := customerSays(t, e, "u1", "m2", "今天有開嗎")
	e.OnInbound(conv, m2)
	settle(e)
	if fs.count() != 1 {
		t.Fatalf("calls = %d, want 1", fs.count())
	}
	if v := current(t, e, conv); v.Status != SuggestionReady || v.ForMessageID != m2.ID {
		t.Fatalf("view %+v", v)
	}
	if len(fs.turns) != 2 || fs.turns[1].Text != "今天有開嗎" {
		t.Fatalf("turns %+v", fs.turns)
	}
	e.Suggest.Schedule(conv, m2.ID)
	e.Suggest.Schedule(conv, m2.ID)
	e.Suggest.Schedule(conv, m1.ID) // 已不是最後一則
	settle(e)
	if fs.count() != 1 {
		t.Fatalf("同一則只產生一次：calls = %d", fs.count())
	}
}

// 新顧客訊息：舊的建議立即作廢（GET 不回過期的），排程後是 pending，完成才 ready；真人回覆後 GET 回 none。
func TestBgSuggestNewMessageInvalidates(t *testing.T) {
	e, _, fs, acc := setupBG(t)
	ctx := context.Background()
	setAutoReply(t, e, acc, func(s *Settings) { s.AutoReplyEnabled = false })
	conv, m1 := inbound(t, e, "u1", "m1", "第一個問題")
	if v := current(t, e, conv); v.Status != SuggestionReady || v.ForMessageID != m1.ID {
		t.Fatalf("view %+v", v)
	}
	fs.mu.Lock()
	fs.block = make(chan struct{})
	fs.mu.Unlock()
	_, m2 := customerSays(t, e, "u1", "m2", "第二個問題")
	if v := current(t, e, conv); v.Status != SuggestionNone || v.ForMessageID != m2.ID {
		t.Fatalf("新訊息後不得回舊的建議：%+v", v)
	}
	e.OnInbound(conv, m2)
	deadline := time.Now().Add(3 * time.Second)
	for current(t, e, conv).Status != SuggestionPending {
		if time.Now().After(deadline) {
			t.Fatalf("應進入 pending：%+v", current(t, e, conv))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if v := current(t, e, conv); v.ForMessageID != m2.ID || len(v.Options) != 0 {
		t.Fatalf("pending view %+v", v)
	}
	close(fs.block)
	settle(e)
	if v := current(t, e, conv); v.Status != SuggestionReady || v.ForMessageID != m2.ID {
		t.Fatalf("view %+v", v)
	}
	var n int
	if err := e.Store.DB.QueryRow(ctx, `SELECT count(*) FROM conversation_suggestions`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("每個對話只留一筆：%d %v", n, err)
	}
	if _, err := e.Inbox.Send(ctx, conv, "店員回覆", "human"); err != nil {
		t.Fatal(err)
	}
	if v := current(t, e, conv); v.Status != SuggestionNone || v.ForMessageID != 0 {
		t.Fatalf("真人回過就沒有需要回覆的訊息：%+v", v)
	}
}

// 模型回 0 個版本自動重試一次；兩次都空 → failed（中文原因）並推事件。
func TestBgSuggestEmptyRetry(t *testing.T) {
	t.Run("retry succeeds", func(t *testing.T) {
		e, _, fs, acc := setupBG(t)
		fs.resps = []Suggestion{{}, okSuggestion()}
		setAutoReply(t, e, acc, func(s *Settings) { s.AutoReplyEnabled = false })
		conv, _ := inbound(t, e, "u1", "m1", "嗨")
		if fs.count() != 2 || current(t, e, conv).Status != SuggestionReady {
			t.Fatalf("calls %d view %+v", fs.count(), current(t, e, conv))
		}
	})
	t.Run("still empty", func(t *testing.T) {
		e, _, fs, acc := setupBG(t)
		fs.resps = []Suggestion{{}}
		setAutoReply(t, e, acc, func(s *Settings) { s.AutoReplyEnabled = false })
		events, unsub := e.Inbox.Hub.Subscribe()
		defer unsub()
		conv, _ := inbound(t, e, "u1", "m1", "嗨")
		v := current(t, e, conv)
		if fs.count() != 2 || v.Status != SuggestionFailed || v.Error != suggestErrEmpty || len(v.Options) != 0 {
			t.Fatalf("calls %d view %+v", fs.count(), v)
		}
		if len(suggestionEvents(eventsOf(events))) != 1 {
			t.Fatal("失敗也要推事件讓前端更新")
		}
	})
	t.Run("refused", func(t *testing.T) {
		e, _, fs, acc := setupBG(t)
		fs.err = ErrRefused
		setAutoReply(t, e, acc, func(s *Settings) { s.AutoReplyEnabled = false })
		conv, _ := inbound(t, e, "u1", "m1", "嗨")
		if v := current(t, e, conv); fs.count() != 1 || v.Status != SuggestionFailed || v.Error != suggestErrRefused {
			t.Fatalf("calls %d view %+v", fs.count(), v)
		}
	})
	t.Run("api error", func(t *testing.T) {
		e, _, fs, acc := setupBG(t)
		fs.err = errors.New("overloaded")
		setAutoReply(t, e, acc, func(s *Settings) { s.AutoReplyEnabled = false })
		conv, _ := inbound(t, e, "u1", "m1", "嗨")
		if v := current(t, e, conv); v.Status != SuggestionFailed || v.Error != suggestErrCall {
			t.Fatalf("view %+v", v)
		}
		if last, fails := e.Suggest.Calls.Last(); last == nil || last.OK || fails != 1 {
			t.Fatalf("失敗也計入 Calls：%+v %d", last, fails)
		}
	})
}

// 每帳號每小時背景產生上限：超過就不產生（none）。
func TestBgSuggestHourlyCap(t *testing.T) {
	e, _, fs, acc := setupBG(t)
	e.Suggest.HourlyCap = 2
	setAutoReply(t, e, acc, func(s *Settings) { s.AutoReplyEnabled = false })
	c1, _ := inbound(t, e, "u1", "m1", "一")
	c2, _ := inbound(t, e, "u2", "m2", "二")
	c3, _ := inbound(t, e, "u3", "m3", "三")
	if fs.count() != 2 || current(t, e, c1).Status != SuggestionReady || current(t, e, c2).Status != SuggestionReady ||
		current(t, e, c3).Status != SuggestionNone {
		t.Fatalf("calls %d c3 %+v", fs.count(), current(t, e, c3))
	}
	// 一小時後恢復
	e.Suggest.Now = func() time.Time { return time.Now().Add(61 * time.Minute) }
	_, m4 := inbound(t, e, "u3", "m4", "還在嗎")
	if fs.count() != 3 || current(t, e, c3).Status != SuggestionReady || current(t, e, c3).ForMessageID != m4.ID {
		t.Fatalf("calls %d c3 %+v", fs.count(), current(t, e, c3))
	}
}

// 同時最多 N 個背景呼叫，其餘排隊。
func TestBgSuggestConcurrencyLimit(t *testing.T) {
	e, _, fs, acc := setupBG(t)
	e.Suggest.Concurrency = 2
	setAutoReply(t, e, acc, func(s *Settings) { s.AutoReplyEnabled = false })
	fs.block = make(chan struct{})
	var convs []int64
	for i, u := range []string{"u1", "u2", "u3", "u4"} {
		c, m := customerSays(t, e, u, "m"+u, "問題")
		convs = append(convs, c)
		e.OnInbound(c, m)
		_ = i
	}
	e.bg.Wait()
	deadline := time.Now().Add(3 * time.Second)
	for fs.count() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("應有 2 個在跑")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if n := fs.count(); n != 2 {
		t.Fatalf("同時最多 2 個，實際已呼叫 %d", n)
	}
	pending := 0
	for _, c := range convs {
		if current(t, e, c).Status == SuggestionPending {
			pending++
		}
	}
	if pending != 4 {
		t.Fatalf("排隊中的也應是 pending：%d", pending)
	}
	close(fs.block)
	settle(e)
	if fs.count() != 4 || fs.maxRun != 2 {
		t.Fatalf("calls %d maxRun %d", fs.count(), fs.maxRun)
	}
}

// 沒設 Claude key（地端模擬）：不產生、不報錯、不留 pending。
func TestBgSuggestNoKey(t *testing.T) {
	e, _, fs, acc := setupBG(t)
	fs.disabled = true
	setAutoReply(t, e, acc, func(s *Settings) { s.AutoReplyEnabled = false })
	conv, _ := inbound(t, e, "u1", "m1", "嗨")
	if fs.count() != 0 || current(t, e, conv).Status != SuggestionNone {
		t.Fatalf("calls %d view %+v", fs.count(), current(t, e, conv))
	}
	e.Suggest.AI = &LiveAI{} // 正式的 LiveAI 沒 key
	inbound(t, e, "u1", "m2", "嗨嗨")
	if current(t, e, conv).Status != SuggestionNone {
		t.Fatal("沒 key 不產生")
	}
}

// 重啟：上次留下的 pending 由 Resume 重新排程。
func TestBgSuggestResume(t *testing.T) {
	e, _, fs, _ := setupBG(t)
	ctx := context.Background()
	conv, m := customerSays(t, e, "u1", "m1", "嗨")
	if err := e.Inbox.SetHandler(ctx, conv, "human"); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.markSuggestionPending(ctx, conv, m.ID); err != nil {
		t.Fatal(err)
	}
	e.Suggest.Resume(ctx)
	settle(e)
	if fs.count() != 1 || current(t, e, conv).Status != SuggestionReady {
		t.Fatalf("calls %d view %+v", fs.count(), current(t, e, conv))
	}
}

func TestSuggestTurnsSkipSystem(t *testing.T) {
	turns := SuggestTurns([]inbox.Message{
		{Direction: "in", SenderType: "contact", Kind: "text", Text: "嗨"},
		{Direction: "out", SenderType: "system", Kind: "system", Text: "真人 15 分鐘未回覆，已交回 AI"},
		{Direction: "in", SenderType: "contact", Kind: "image"},
		{Direction: "in", SenderType: "contact", Kind: "text", Deleted: true},
	})
	if len(turns) != 3 || turns[1].Text != "（顧客傳了一則圖片，沒有文字）" || turns[2].Text != "" {
		t.Fatalf("turns %+v", turns)
	}
}
