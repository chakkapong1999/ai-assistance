// Package webhook verifies and decodes Bitbucket Cloud webhook deliveries.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const signaturePrefix = "sha256="

// Verify reports whether header ("sha256=<hex>", the X-Hub-Signature value)
// is the HMAC-SHA256 of body under secret. The comparison is constant-time.
// Any malformed header simply fails verification.
func Verify(secret, body []byte, header string) bool {
	if len(secret) == 0 || !strings.HasPrefix(header, signaturePrefix) {
		return false
	}
	got, err := hex.DecodeString(strings.TrimPrefix(header, signaturePrefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}
