// Package account defines provider identities for explicit local enrollment.
// Account administration is separate from the immutable memory journal.
package account

import (
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid account identity")

type Identity struct {
	Issuer  string
	Subject string
}

func (i Identity) Validate() error {
	u, err := url.Parse(i.Issuer)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		len(i.Issuer) > 2048 || strings.TrimSpace(i.Issuer) != i.Issuer || !utf8.ValidString(i.Issuer) || strings.ContainsRune(i.Issuer, 0) {
		return ErrInvalid
	}
	if i.Subject == "" || len(i.Subject) > 255 || strings.TrimSpace(i.Subject) != i.Subject ||
		!utf8.ValidString(i.Subject) || strings.ContainsRune(i.Subject, 0) {
		return ErrInvalid
	}
	return nil
}
