package secrets

import (
	"errors"
	"strings"
	"testing"
)

func mustBox(t *testing.T, key string) *Box {
	t.Helper()
	b, err := FromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRoundTrip(t *testing.T) {
	b := mustBox(t, "k1")
	for _, plain := range []string{"sk-ant-api03-abcdef", "", "中文 secret ✓", strings.Repeat("x", 5000)} {
		enc, err := b.Encrypt(plain, "anthropic_api_key")
		if err != nil {
			t.Fatal(err)
		}
		if plain != "" && strings.Contains(enc, plain) {
			t.Fatalf("密文含明文: %q", enc)
		}
		got, err := b.Decrypt(enc, "anthropic_api_key")
		if err != nil || got != plain {
			t.Fatalf("round trip: got %q err %v, want %q", got, err, plain)
		}
	}
}

func TestEncryptIsRandomized(t *testing.T) {
	b := mustBox(t, "k1")
	a, _ := b.Encrypt("same", "x")
	c, _ := b.Encrypt("same", "x")
	if a == c {
		t.Fatal("同明文兩次加密密文相同：nonce 沒隨機")
	}
}

func TestWrongKeyFails(t *testing.T) {
	enc, _ := mustBox(t, "k1").Encrypt("secret", "x")
	if _, err := mustBox(t, "k2").Decrypt(enc, "x"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("wrong key: err = %v", err)
	}
}

func TestWrongAADFails(t *testing.T) {
	b := mustBox(t, "k1")
	enc, _ := b.Encrypt("secret", "ig_app_secret")
	if _, err := b.Decrypt(enc, "ig_verify_token"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("wrong aad: err = %v", err)
	}
}

func TestTamperFails(t *testing.T) {
	b := mustBox(t, "k1")
	enc, _ := b.Encrypt("secret-value", "x")
	// 改尾端（落在 tag）、改中間（落在密文／nonce）、截斷、錯誤前綴、非 base64、無前綴、空字串
	flip := func(s string, i int) string {
		c := byte('A')
		if s[i] == 'A' {
			c = 'B'
		}
		return s[:i] + string(c) + s[i+1:]
	}
	cases := map[string]string{
		"tag":       flip(enc, len(enc)-2),
		"body":      flip(enc, len(prefix)+20),
		"nonce":     flip(enc, len(prefix)+2),
		"truncated": enc[:len(enc)-8],
		"prefix":    "v2:" + strings.TrimPrefix(enc, prefix),
		"garbage":   prefix + "!!!!",
		"plain":     "secret-value",
		"empty":     "",
	}
	for name, bad := range cases {
		if got, err := b.Decrypt(bad, "x"); !errors.Is(err, ErrDecrypt) {
			t.Errorf("%s: 應解密失敗, got %q err %v", name, got, err)
		}
	}
}

func TestNewBoxFallback(t *testing.T) {
	jwt := "jwt-secret-1234567890"
	b1, fb, err := NewBox("", jwt)
	if err != nil || !fb {
		t.Fatalf("fallback=%v err=%v", fb, err)
	}
	b2, fb, err := NewBox("explicit", jwt)
	if err != nil || fb {
		t.Fatalf("explicit fallback=%v err=%v", fb, err)
	}
	enc, _ := b1.Encrypt("v", "x")
	if _, err := b2.Decrypt(enc, "x"); err == nil {
		t.Fatal("SECRET_KEY 與衍生金鑰不該互通")
	}
	// 衍生金鑰不等於直接拿 JWT_SECRET 當 SECRET_KEY
	b3, _, _ := NewBox(jwt, "")
	if _, err := b3.Decrypt(enc, "x"); err == nil {
		t.Fatal("衍生金鑰不該等於 JWT_SECRET 直接雜湊")
	}
	// 同一個 JWT_SECRET 衍生結果穩定
	b4, _, _ := NewBox("", jwt)
	if got, err := b4.Decrypt(enc, "x"); err != nil || got != "v" {
		t.Fatalf("衍生不穩定: %q %v", got, err)
	}
	if _, _, err := NewBox("", ""); err == nil {
		t.Fatal("兩者皆空應回錯誤")
	}
}
