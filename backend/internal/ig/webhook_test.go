package ig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"object":"instagram"}`)
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write(body)
	good := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if !VerifySignature("secret", body, good) {
		t.Fatal("valid signature rejected")
	}
	for _, c := range []struct{ secret, header string }{
		{"secret", "sha256=deadbeef"},
		{"other", good},
		{"", good},
		{"secret", ""},
		{"secret", "sha1=" + good[7:]},
	} {
		if VerifySignature(c.secret, body, c.header) {
			t.Fatalf("accepted bad signature: %+v", c)
		}
	}
}
