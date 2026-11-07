package sqlite

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/VVivid/vvivid-workspace/ArtificialMemory/memory"
)

const exhaustedSupport = -1

// A continuation finishes supporting context/links for a fixed match page
// before advancing MatchOffset. ContextOffset and LinkOffset count support rows
// emitted for that page; exhaustedSupport marks a completed support stream.
type continuation struct {
	Version       int    `json:"v"`
	QueryHash     string `json:"q"`
	Snapshot      int64  `json:"s"`
	MatchOffset   int64  `json:"m"`
	ContextOffset int64  `json:"c"`
	LinkOffset    int64  `json:"l"`
	SupportOnly   bool   `json:"p"` // Matches were already emitted for this match page.
}

func queryHash(account string, q memory.Query) string {
	q.Cursor = ""
	data, _ := json.Marshal(struct {
		Account string
		Query   memory.Query
	}{account, q})
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func (s *Store) encodeCursor(c continuation) string {
	data, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, s.cursorKey)
	mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Store) decodeCursor(raw, hash string) (continuation, error) {
	var c continuation
	body, signature, ok := strings.Cut(raw, ".")
	if !ok {
		return c, memory.ErrCursor
	}
	data, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return c, memory.ErrCursor
	}
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return c, memory.ErrCursor
	}
	mac := hmac.New(sha256.New, s.cursorKey)
	mac.Write(data)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return c, memory.ErrCursor
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, memory.ErrCursor
	}
	if c.Version != 1 || c.QueryHash != hash {
		return c, memory.ErrCursor
	}
	if c.Snapshot < 0 || c.MatchOffset < 0 || c.ContextOffset < exhaustedSupport || c.LinkOffset < exhaustedSupport {
		return c, memory.ErrCursor
	}
	return c, nil
}
