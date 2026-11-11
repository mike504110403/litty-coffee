// Package appsettings 是「後台可改、立即生效」的執行期設定：Meta App 憑證（含 Facebook 登入配置 ID）與 Claude AI 設定。
//
// 優先序：DB（app_settings 表）> .env。secret 類欄位以 AES-256-GCM 加密後存 DB（見 internal/secrets）。
// Store 持有一份「有效值」快照（RWMutex 保護、讀取回傳複本）；儲存時先寫 DB，成功才換快照並通知監聽者。
// 使用者（OAuth client、webhook 驗簽、AI）每次使用時讀快照，因此不需重啟。
package appsettings

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mike504110403/litty-coffee/backend/internal/secrets"
)

// 設定 key（同時是 DB 的 key 與加密的 AAD）。
const (
	KeyIGAppID       = "ig_app_id"
	KeyIGAppSecret   = "ig_app_secret"
	KeyIGConfigID    = "ig_config_id"
	KeyIGVerifyToken = "ig_verify_token"
	KeyAnthropicKey  = "anthropic_api_key"
	KeyAIModel       = "ai_model"
	KeyAIEffort      = "ai_effort"
)

// Field 描述一個可設定欄位。Secret＝加密存放，API 只回尾 4 碼。
type Field struct {
	Key    string
	Secret bool
}

// Fields 依顯示順序列出全部欄位。
// IG_VERIFY_TOKEN 也當 secret：握手用的共享字串，保守處理（加密＋不回傳全文）。
var Fields = []Field{
	{KeyIGAppID, false},
	{KeyIGAppSecret, true},
	{KeyIGConfigID, false},
	{KeyIGVerifyToken, true},
	{KeyAnthropicKey, true},
	{KeyAIModel, false},
	{KeyAIEffort, false},
}

func fieldOf(key string) (Field, bool) {
	for _, f := range Fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

// Source 表示有效值的來源。
const (
	SourceDB   = "db"
	SourceEnv  = "env"
	SourceNone = "none"
)

// Values 是一份有效值快照（DB 優先，其次 .env）。
type Values struct {
	IGAppID       string
	IGAppSecret   string
	IGConfigID    string // Facebook 登入的「配置 ID」（Unified Login 授權網址的 config_id）
	IGVerifyToken string
	AnthropicKey  string
	AIModel       string
	AIEffort      string
}

func (v *Values) set(key, val string) {
	switch key {
	case KeyIGAppID:
		v.IGAppID = val
	case KeyIGAppSecret:
		v.IGAppSecret = val
	case KeyIGConfigID:
		v.IGConfigID = val
	case KeyIGVerifyToken:
		v.IGVerifyToken = val
	case KeyAnthropicKey:
		v.AnthropicKey = val
	case KeyAIModel:
		v.AIModel = val
	case KeyAIEffort:
		v.AIEffort = val
	}
}

func (v Values) get(key string) string {
	switch key {
	case KeyIGAppID:
		return v.IGAppID
	case KeyIGAppSecret:
		return v.IGAppSecret
	case KeyIGConfigID:
		return v.IGConfigID
	case KeyIGVerifyToken:
		return v.IGVerifyToken
	case KeyAnthropicKey:
		return v.AnthropicKey
	case KeyAIModel:
		return v.AIModel
	case KeyAIEffort:
		return v.AIEffort
	}
	return ""
}

// Entry 是單一欄位的有效狀態。
type Entry struct {
	Key    string
	Secret bool
	Value  string // 有效值（呼叫端對 secret 不得原樣回給前端）
	Source string // db | env | none
}

// Set 欄位有沒有值。
func (e Entry) Set() bool { return e.Value != "" }

// Hint 回尾 4 碼（值不足 8 碼時不洩漏任何字元，回空字串）。
func (e Entry) Hint() string {
	r := []rune(e.Value)
	if len(r) < 8 {
		return ""
	}
	return string(r[len(r)-4:])
}

// Store 是設定的唯一入口。零值不可用，請用 New。
type Store struct {
	pool *pgxpool.Pool
	box  *secrets.Box
	env  Values

	writeMu   sync.Mutex // 序列化 Update（DB 寫入＋換快照＋通知），保證監聽者看到的順序與寫入順序一致
	mu        sync.RWMutex
	db        map[string]string // 解密後的 DB 值
	eff       Values
	listeners []func(Values)
}

// New 建 Store；env 是 .env 的值（含預設值）。需再呼叫 Load 讀 DB。
func New(pool *pgxpool.Pool, box *secrets.Box, env Values) *Store {
	s := &Store{pool: pool, box: box, env: env, db: map[string]string{}}
	s.eff = s.compute()
	return s
}

// compute 由 env＋db 算有效值。呼叫者需持有 mu 或尚未共享。
func (s *Store) compute() Values {
	v := s.env
	for k, val := range s.db {
		if val != "" {
			v.set(k, val)
		}
	}
	return v
}

// Load 從 DB 讀全部設定並解密。解不開的欄位（SECRET_KEY 換過）略過並記 log，該欄位退回 .env。
func (s *Store) Load(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT key, value FROM app_settings`)
	if err != nil {
		return err
	}
	defer rows.Close()
	loaded := map[string]string{}
	for rows.Next() {
		var k, raw string
		if err := rows.Scan(&k, &raw); err != nil {
			return err
		}
		f, ok := fieldOf(k)
		if !ok {
			continue
		}
		val := raw
		if f.Secret {
			if val, err = s.box.Decrypt(raw, k); err != nil {
				log.Printf("appsettings: %s 解密失敗，改用 .env（SECRET_KEY 是否換過？）", k)
				continue
			}
		}
		loaded[k] = val
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.db = loaded
	s.eff = s.compute()
	s.mu.Unlock()
	return nil
}

// Values 回目前有效值的複本。
func (s *Store) Values() Values {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.eff
}

// Entries 回全部欄位的有效狀態（依 Fields 順序）。
func (s *Store) Entries() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Entry, 0, len(Fields))
	for _, f := range Fields {
		e := Entry{Key: f.Key, Secret: f.Secret, Source: SourceNone}
		if v := s.db[f.Key]; v != "" {
			e.Value, e.Source = v, SourceDB
		} else if v := s.env.get(f.Key); v != "" {
			e.Value, e.Source = v, SourceEnv
		}
		out = append(out, e)
	}
	return out
}

// OnChange 註冊監聽者：每次 Update 成功後以新快照呼叫（在 Update 的呼叫緒內、序列化執行）。
// 監聽者不得回頭呼叫 Update。
func (s *Store) OnChange(fn func(Values)) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.listeners = append(s.listeners, fn)
}

// ErrInvalid 包裝驗證錯誤，訊息可直接回給使用者。
type ErrInvalid struct{ Msg string }

func (e *ErrInvalid) Error() string { return e.Msg }

func invalid(format string, a ...any) error { return &ErrInvalid{Msg: fmt.Sprintf(format, a...)} }

// Validate 檢查單一欄位的值格式（值已 trim、非空）。
func Validate(key, val string) error {
	if strings.ContainsAny(val, " \t\r\n") {
		return invalid("%s 不可含空白", label(key))
	}
	if len(val) > 300 {
		return invalid("%s 太長", label(key))
	}
	switch key {
	case KeyIGAppID, KeyIGConfigID:
		for _, r := range val {
			if r < '0' || r > '9' {
				return invalid("%s 應為純數字", label(key))
			}
		}
	case KeyAIEffort:
		switch val {
		case "low", "medium", "high":
		default:
			return invalid("推理強度只能是 low、medium 或 high")
		}
	}
	return nil
}

func label(key string) string {
	switch key {
	case KeyIGAppID:
		return "Meta App ID"
	case KeyIGAppSecret:
		return "Meta App Secret"
	case KeyIGConfigID:
		return "配置 ID"
	case KeyIGVerifyToken:
		return "Webhook 驗證 Token"
	case KeyAnthropicKey:
		return "Claude API Key"
	case KeyAIModel:
		return "模型"
	case KeyAIEffort:
		return "推理強度"
	}
	return key
}

// Update 套用變更：set 的值寫入（覆蓋），clear 的 key 刪除 DB 值（退回 .env）。
// 值為空字串的 set 項略過（＝不變）。全部驗證通過、DB 寫入成功（單一交易）後才換快照並通知監聽者。
func (s *Store) Update(ctx context.Context, set map[string]string, clear []string) error {
	clean := map[string]string{}
	for k, v := range set {
		if _, ok := fieldOf(k); !ok {
			return invalid("未知欄位：%s", k)
		}
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if err := Validate(k, v); err != nil {
			return err
		}
		clean[k] = v
	}
	clearSet := map[string]bool{}
	for _, k := range clear {
		if _, ok := fieldOf(k); !ok {
			return invalid("未知欄位：%s", k)
		}
		if _, dup := clean[k]; dup {
			return invalid("%s 不能同時設定又清除", label(k))
		}
		clearSet[k] = true
	}
	if len(clean) == 0 && len(clearSet) == 0 {
		return nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	stored := map[string]string{}
	for k, v := range clean {
		f, _ := fieldOf(k)
		if f.Secret {
			enc, err := s.box.Encrypt(v, k)
			if err != nil {
				return err
			}
			stored[k] = enc
		} else {
			stored[k] = v
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for k, v := range stored {
		if _, err := tx.Exec(ctx, `
INSERT INTO app_settings (key, value, updated_at) VALUES ($1, $2, now())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, k, v); err != nil {
			return err
		}
	}
	for k := range clearSet {
		if _, err := tx.Exec(ctx, `DELETE FROM app_settings WHERE key = $1`, k); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	s.mu.Lock()
	for k, v := range clean {
		s.db[k] = v
	}
	for k := range clearSet {
		delete(s.db, k)
	}
	s.eff = s.compute()
	snap := s.eff
	s.mu.Unlock()

	for _, fn := range s.listeners {
		fn(snap)
	}
	return nil
}

var ErrNotConfigured = errors.New("未設定 Meta App ID／App Secret／配置 ID")
