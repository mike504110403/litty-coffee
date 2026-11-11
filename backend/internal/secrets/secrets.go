// Package secrets 提供 AES-256-GCM 加解密，用來把 API key 之類的 secret 加密後存進 DB。
//
// 金鑰來源：env SECRET_KEY（任意字串，SHA-256 雜湊成 32 bytes）。
// MVP 兜底：SECRET_KEY 沒設時，由 JWT_SECRET 加固定標籤雜湊衍生（NewBox 回傳 fallback=true，呼叫端要記警告）。
// 兜底的代價：換 JWT_SECRET 會讓已存的密文全部解不開（讀取時會被略過、退回 .env）；
// 正式環境應該設獨立的 SECRET_KEY，且之後不要換。
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// 密文格式：prefix + base64url(nonce || ciphertext||tag)。prefix 方便日後換演算法。
const prefix = "v1:"

// 從 JWT_SECRET 衍生金鑰時加的固定標籤，讓衍生出的金鑰和 JWT 簽章用的原始 secret 不同。
const fallbackLabel = "litty-coffee/app-settings/v1\x00"

var ErrDecrypt = errors.New("secrets: 解密失敗（金鑰不符或密文被竄改）")

// Box 持有一把 32 bytes 金鑰；可並行使用（cipher.AEAD 無可變狀態）。
type Box struct {
	aead cipher.AEAD
}

// NewBox 依 SECRET_KEY 建 Box；secretKey 為空時改由 jwtSecret 衍生並回傳 fallback=true。兩者皆空回錯誤。
func NewBox(secretKey, jwtSecret string) (box *Box, fallback bool, err error) {
	switch {
	case secretKey != "":
		box, err = FromKey(secretKey)
		return box, false, err
	case jwtSecret != "":
		box, err = FromKey(fallbackLabel + jwtSecret)
		return box, true, err
	}
	return nil, false, errors.New("secrets: SECRET_KEY 與 JWT_SECRET 皆未設定")
}

// FromKey 把任意字串 SHA-256 成 32 bytes 當 AES-256 金鑰。
func FromKey(key string) (*Box, error) {
	if key == "" {
		return nil, errors.New("secrets: 金鑰不可為空")
	}
	sum := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Encrypt 加密 plaintext。aad 是綁定用的脈絡（用設定 key 名稱），
// 解密時必須給同一個，防止把 A 欄位的密文搬去 B 欄位。每次呼叫用新的隨機 nonce。
func (b *Box) Encrypt(plaintext, aad string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secrets: nonce: %w", err)
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plaintext), []byte(aad))
	return prefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Decrypt 還原 Encrypt 的結果；格式不符、金鑰不對、aad 不同或被竄改都回 ErrDecrypt。
func (b *Box) Decrypt(token, aad string) (string, error) {
	if !strings.HasPrefix(token, prefix) {
		return "", ErrDecrypt
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, prefix))
	if err != nil || len(raw) < b.aead.NonceSize()+b.aead.Overhead() {
		return "", ErrDecrypt
	}
	n := b.aead.NonceSize()
	plain, err := b.aead.Open(nil, raw[:n], raw[n:], []byte(aad))
	if err != nil {
		return "", ErrDecrypt
	}
	return string(plain), nil
}
