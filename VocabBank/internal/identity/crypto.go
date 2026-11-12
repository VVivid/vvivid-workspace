package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

func (k *Keyring) seal(value any, purpose string) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	a := k.keys[k.active]
	nonce := make([]byte, a.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	encrypted := a.Seal(nonce, nonce, raw, []byte(purpose))
	return []byte(k.active + ":" + base64.RawURLEncoding.EncodeToString(encrypted)), nil
}
func (k *Keyring) open(raw []byte, purpose string, value any) error {
	name, data, ok := strings.Cut(string(raw), ":")
	a := k.keys[name]
	cipher, err := base64.RawURLEncoding.DecodeString(data)
	if !ok || err != nil || a == nil || len(cipher) < a.NonceSize()+a.Overhead() {
		return ErrUnauthenticated
	}
	plain, err := a.Open(nil, cipher[:a.NonceSize()], cipher[a.NonceSize():], []byte(purpose))
	if err != nil {
		return ErrUnauthenticated
	}
	return json.Unmarshal(plain, value)
}
func hash(value string) []byte { v := sha256.Sum256([]byte(value)); return v[:] }
func purpose(kind, host string, h []byte) string {
	return fmt.Sprintf("vocabbank:%s:%s:%s", kind, host, hex.EncodeToString(h))
}
func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
