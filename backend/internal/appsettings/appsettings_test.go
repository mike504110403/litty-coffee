package appsettings

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mike504110403/litty-coffee/backend/internal/db"
	"github.com/mike504110403/litty-coffee/backend/internal/secrets"
)

func setupPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if err := db.Migrate(url); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), `TRUNCATE app_settings`); err != nil {
		t.Fatal(err)
	}
	return pool
}

func newStore(t *testing.T, pool *pgxpool.Pool, env Values) *Store {
	t.Helper()
	box, err := secrets.FromKey("test-key")
	if err != nil {
		t.Fatal(err)
	}
	s := New(pool, box, env)
	if err := s.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

var testEnv = Values{IGAppID: "111", IGAppSecret: "env-secret-aaaa", AIModel: "env-model", AIEffort: "low"}

func entry(s *Store, key string) Entry {
	for _, e := range s.Entries() {
		if e.Key == key {
			return e
		}
	}
	return Entry{}
}

func TestDBOverridesEnvAndClearFallsBack(t *testing.T) {
	pool := setupPool(t)
	s := newStore(t, pool, testEnv)
	ctx := context.Background()

	if e := entry(s, KeyIGAppID); e.Source != SourceEnv || e.Value != "111" {
		t.Fatalf("初始應來自 env: %+v", e)
	}
	if e := entry(s, KeyAnthropicKey); e.Source != SourceNone || e.Set() {
		t.Fatalf("未設應為 none: %+v", e)
	}

	err := s.Update(ctx, map[string]string{KeyIGAppID: "222", KeyAnthropicKey: "sk-ant-abcdefgh1234", KeyAIModel: ""}, nil)
	if err != nil {
		t.Fatal(err)
	}
	v := s.Values()
	if v.IGAppID != "222" || v.AnthropicKey != "sk-ant-abcdefgh1234" || v.AIModel != "env-model" {
		t.Fatalf("values: %+v", v)
	}
	if e := entry(s, KeyAnthropicKey); e.Source != SourceDB || e.Hint() != "1234" {
		t.Fatalf("entry: %+v hint=%q", e, e.Hint())
	}

	// 重新載入（模擬重啟）後 DB 值仍在
	s2 := newStore(t, pool, testEnv)
	if got := s2.Values(); got.IGAppID != "222" || got.AnthropicKey != "sk-ant-abcdefgh1234" {
		t.Fatalf("reload: %+v", got)
	}

	// clear 退回 env；沒有 env 的欄位變 none
	if err := s.Update(ctx, nil, []string{KeyIGAppID, KeyAnthropicKey}); err != nil {
		t.Fatal(err)
	}
	if v := s.Values(); v.IGAppID != "111" || v.AnthropicKey != "" {
		t.Fatalf("after clear: %+v", v)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM app_settings`).Scan(&n)
	if n != 0 {
		t.Fatalf("DB 列沒刪乾淨: %d", n)
	}
}

func TestSecretsEncryptedAtRest(t *testing.T) {
	pool := setupPool(t)
	s := newStore(t, pool, Values{})
	ctx := context.Background()
	if err := s.Update(ctx, map[string]string{KeyIGAppSecret: "plain-app-secret-XYZ", KeyIGAppID: "999", KeyAIEffort: "high"}, nil); err != nil {
		t.Fatal(err)
	}
	raw := func(k string) string {
		var v string
		if err := pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1`, k).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if got := raw(KeyIGAppSecret); strings.Contains(got, "plain-app-secret") || !strings.HasPrefix(got, "v1:") {
		t.Fatalf("secret 應為密文: %q", got)
	}
	if raw(KeyIGAppID) != "999" || raw(KeyAIEffort) != "high" {
		t.Fatal("非 secret 應為明文")
	}
}

func TestUndecryptableFallsBackToEnv(t *testing.T) {
	pool := setupPool(t)
	s := newStore(t, pool, testEnv)
	if err := s.Update(context.Background(), map[string]string{KeyIGAppSecret: "db-secret-12345678"}, nil); err != nil {
		t.Fatal(err)
	}
	// 換一把金鑰重載：解不開 → 退回 env，且不報錯
	box2, _ := secrets.FromKey("another-key")
	s2 := New(pool, box2, testEnv)
	if err := s2.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e := entry(s2, KeyIGAppSecret); e.Source != SourceEnv || e.Value != "env-secret-aaaa" {
		t.Fatalf("應退回 env: %+v", e)
	}
	// 搬密文到別的 key 也解不開（AAD 綁 key 名）
	if _, err := pool.Exec(context.Background(),
		`UPDATE app_settings SET key = $1 WHERE key = $2`, KeyIGVerifyToken, KeyIGAppSecret); err != nil {
		t.Fatal(err)
	}
	s3 := newStore(t, pool, Values{})
	if s3.Values().IGVerifyToken != "" {
		t.Fatal("密文被搬到別的欄位不該解得開")
	}
}

func TestValidation(t *testing.T) {
	pool := setupPool(t)
	s := newStore(t, pool, testEnv)
	ctx := context.Background()
	bad := []map[string]string{
		{KeyIGAppID: "12ab"},
		{KeyAIEffort: "extreme"},
		{KeyIGAppSecret: "has space"},
		{"nope": "x"},
		{KeyAnthropicKey: strings.Repeat("k", 400)},
	}
	for _, in := range bad {
		var inv *ErrInvalid
		if err := s.Update(ctx, in, nil); !errors.As(err, &inv) {
			t.Errorf("%v: 應為 ErrInvalid, got %v", in, err)
		}
	}
	var inv *ErrInvalid
	if err := s.Update(ctx, map[string]string{KeyAIModel: "m"}, []string{KeyAIModel}); !errors.As(err, &inv) {
		t.Errorf("同時 set＋clear 應拒絕: %v", err)
	}
	if err := s.Update(ctx, nil, []string{"nope"}); !errors.As(err, &inv) {
		t.Errorf("clear 未知欄位應拒絕: %v", err)
	}
	// 失敗的更新不得留下任何變動（含同批中合法的欄位）
	if err := s.Update(ctx, map[string]string{KeyIGAppID: "555", KeyAIEffort: "bad"}, nil); err == nil {
		t.Fatal("應失敗")
	}
	if s.Values().IGAppID != "111" {
		t.Fatal("驗證失敗不該改動快照")
	}
}

func TestOnChangeAndHint(t *testing.T) {
	pool := setupPool(t)
	s := newStore(t, pool, Values{})
	var got []Values
	s.OnChange(func(v Values) { got = append(got, v) })
	_ = s.Update(context.Background(), map[string]string{KeyAIModel: "m1"}, nil)
	_ = s.Update(context.Background(), map[string]string{}, nil) // 無變更不通知
	_ = s.Update(context.Background(), map[string]string{KeyAIModel: "m2"}, nil)
	if len(got) != 2 || got[0].AIModel != "m1" || got[1].AIModel != "m2" {
		t.Fatalf("通知: %+v", got)
	}
	if h := (Entry{Value: "short"}).Hint(); h != "" {
		t.Fatalf("短值不得洩漏尾碼: %q", h)
	}
}

func TestLiveOAuthSwapsCredentialsWithoutRestart(t *testing.T) {
	pool := setupPool(t)
	s := newStore(t, pool, Values{})
	o := &LiveOAuth{Store: s, RedirectURI: "https://x/cb", APIBase: "https://graph.example/v1", SettingsErrorURL: "https://x/settings"}

	if o.Configured() {
		t.Fatal("沒憑證應為未設定")
	}
	if _, err := o.Exchange(context.Background(), "c"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("exchange: %v", err)
	}
	if u := o.AuthorizeURL("st"); !strings.HasPrefix(u, "https://x/settings?ig_error=") {
		t.Fatalf("未設定時應導回設定頁: %s", u)
	}

	ctx := context.Background()
	if err := s.Update(ctx, map[string]string{KeyIGAppID: "123", KeyIGAppSecret: "secret-one-1234"}, nil); err != nil {
		t.Fatal(err)
	}
	if !o.Configured() || !strings.Contains(o.AuthorizeURL("st"), "client_id=123") {
		t.Fatalf("存檔後應立即用新 App ID: %s", o.AuthorizeURL("st"))
	}
	if err := s.Update(ctx, map[string]string{KeyIGAppID: "456"}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(o.AuthorizeURL("st"), "client_id=456") {
		t.Fatal("換 App ID 應立即生效")
	}
	if err := s.Update(ctx, nil, []string{KeyIGAppSecret}); err != nil {
		t.Fatal(err)
	}
	if o.Configured() {
		t.Fatal("清掉 secret 後應回到未設定")
	}
}

// 並行讀寫：go test -race 下不得有 data race。
func TestConcurrentReadWrite(t *testing.T) {
	pool := setupPool(t)
	s := newStore(t, pool, Values{IGAppID: "1", IGAppSecret: "env-secret-1234"})
	o := &LiveOAuth{Store: s, RedirectURI: "https://x/cb", APIBase: "https://g/v1"}
	s.OnChange(func(Values) {})
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = o.AuthorizeURL("s")
				_ = o.Configured()
				_ = s.Values()
				_ = s.Entries()
			}
		}()
	}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				id := []string{"100", "200"}[(i+j)%2]
				if err := s.Update(ctx, map[string]string{KeyIGAppID: id}, nil); err != nil {
					t.Error(err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}
