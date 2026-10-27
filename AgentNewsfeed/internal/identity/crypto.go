package identity

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

func (keys *Keyring) seal(value any, purpose string) ([]byte, error) {
	plaintext, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	aead := keys.keys[keys.active]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ciphertext := aead.Seal(nonce, nonce, plaintext, []byte(purpose))
	return []byte(keys.active + ":" + base64.RawURLEncoding.EncodeToString(ciphertext)), nil
}

func (keys *Keyring) open(encrypted []byte, purpose string, value any) error {
	name, encoded, ok := strings.Cut(string(encrypted), ":")
	aead := keys.keys[name]
	ciphertext, err := base64.RawURLEncoding.DecodeString(encoded)
	if !ok || aead == nil || err != nil || len(ciphertext) < aead.NonceSize()+aead.Overhead() {
		return fmt.Errorf("invalid protected identity state")
	}
	plaintext, err := aead.Open(nil, ciphertext[:aead.NonceSize()], ciphertext[aead.NonceSize():], []byte(purpose))
	if err != nil {
		return fmt.Errorf("invalid protected identity state")
	}
	return json.Unmarshal(plaintext, value)
}
