// Package keyfile reads explicit operator-supplied private key files.
package keyfile

import (
	"errors"
	"io"
	"os"
)

// Load accepts exactly 32 raw bytes from a private regular file. There are no
// generated or default keys; a newline or a hex/base64 representation is invalid.
func Load(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open supplied key file")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("key file must be regular and private (0400 or 0600)")
	}
	key, err := io.ReadAll(io.LimitReader(f, 33))
	if err != nil || len(key) != 32 {
		clear(key)
		return nil, errors.New("key file must contain exactly 32 raw bytes")
	}
	return key, nil
}
