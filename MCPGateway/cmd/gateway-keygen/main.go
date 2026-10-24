// gateway-keygen explicitly creates a signing key and a public backend trust bundle.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/go-jose/go-jose/v4"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	private := flag.String("private", "", "new private JWK file")
	public := flag.String("public", "", "new public JWKS file")
	flag.Parse()
	if *private == "" || *public == "" || *private == *public || flag.NArg() != 0 {
		return errors.New("usage: gateway-keygen --private NEW_FILE --public NEW_FILE")
	}
	key, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return err
	}
	jwk := jose.JSONWebKey{Key: key, KeyID: rand.Text(), Algorithm: "RS256", Use: "sig"}
	// Never overwrite an existing key. Explicit key management is separate from startup.
	for _, entry := range []struct {
		file  string
		value any
		mode  os.FileMode
	}{{*private, jwk, 0600}, {*public, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk.Public()}}, 0644}} {
		f, err := os.OpenFile(entry.file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, entry.mode)
		if err != nil {
			return err
		}
		encodeErr := json.NewEncoder(f).Encode(entry.value)
		closeErr := f.Close()
		if encodeErr != nil {
			return encodeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
