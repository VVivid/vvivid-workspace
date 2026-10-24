package ingress

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
)

const (
	MaxIdempotencyKeyBytes = 128
	MaxTimestampSkew       = 5 * time.Minute
)

func validIdempotencyKey(key string) bool {
	if len(key) == 0 || len(key) > MaxIdempotencyKeyBytes {
		return false
	}
	for _, c := range key {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

func singleHeader(headers http.Header, name string) (string, bool) {
	values := headers.Values(name)
	if len(values) != 1 {
		return "", false
	}
	return values[0], true
}

// HMACSignature returns the X-Webhook-Signature value. The exact signed bytes are
// "WebhookControl/v1\n" + sourceID + "\n" + idempotencyKey + "\n" + timestamp +
// "\n" + rawBody. timestamp is canonical decimal Unix seconds, without padding.
// The body is not decoded, normalized, or followed by an additional newline.
func HMACSignature(secret []byte, sourceID domain.ID, idempotencyKey, timestamp string, rawBody []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("WebhookControl/v1\n" + string(sourceID) + "\n" + idempotencyKey + "\n" + timestamp + "\n"))
	mac.Write(rawBody)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func authenticate(request *http.Request, source domain.Source, secret []byte, key string, raw []byte, now time.Time) bool {
	if len(secret) < 16 || len(secret) > 4096 {
		return false
	}
	if source.Auth.Kind == domain.AuthBearer {
		authorization, ok := singleHeader(request.Header, "Authorization")
		if !ok || len(authorization) < 8 || len(authorization) > 4103 || !strings.EqualFold(authorization[:7], "Bearer ") {
			return false
		}
		expected := sha256.Sum256(secret)
		presented := sha256.Sum256([]byte(authorization[7:]))
		return hmac.Equal(expected[:], presented[:])
	}
	if source.Auth.Kind != domain.AuthHMAC || len(secret) < 32 {
		return false
	}
	timestamp, ok := singleHeader(request.Header, "X-Webhook-Timestamp")
	if !ok || len(timestamp) > 12 {
		return false
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || seconds < 0 || strconv.FormatInt(seconds, 10) != timestamp {
		return false
	}
	instant := time.Unix(seconds, 0)
	if instant.Before(now.Add(-MaxTimestampSkew)) || instant.After(now.Add(MaxTimestampSkew)) {
		return false
	}
	signature, ok := singleHeader(request.Header, "X-Webhook-Signature")
	if !ok || len(signature) != 71 || !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	provided, err := hex.DecodeString(signature[7:])
	if err != nil {
		return false
	}
	expected, _ := hex.DecodeString(HMACSignature(secret, source.ID, key, timestamp, raw)[7:])
	return hmac.Equal(expected, provided)
}

func keyedDigest(secret []byte, purpose string, sourceID domain.ID, data []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("WebhookControl/v1/" + purpose + "\n" + string(sourceID) + "\n"))
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}
