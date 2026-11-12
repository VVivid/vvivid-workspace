package identity

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/coreos/go-oidc/v3/oidc"
)

// RemoteKeySet preserves transport error causes, but its HTTP-status and body-read
// errors have no typed cause. Classify temporary fetch failures at that boundary
// so a failed cryptographic signature is never mistaken for a provider outage.
func providerKeys(client *http.Client, endpoint string) *oidc.RemoteKeySet {
	keyClient := *client
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	keyClient.Transport = signingKeyTransport{transport}
	return oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), &keyClient), endpoint)
}

type signingKeyTransport struct{ base http.RoundTripper }

func (t signingKeyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, ErrUnavailable
	}
	if res.StatusCode >= 500 || res.StatusCode == http.StatusTooManyRequests {
		res.Body.Close()
		return nil, ErrUnavailable
	}
	// Read here because RemoteKeySet formats body-read errors without wrapping.
	// A truncated response is retryable; an intact but invalid JWKS is not.
	raw, err := io.ReadAll(io.LimitReader(res.Body, (256<<10)+1))
	res.Body.Close()
	if err != nil {
		return nil, ErrUnavailable
	}
	if len(raw) > 256<<10 {
		return nil, ErrUnauthenticated
	}
	res.Body = io.NopCloser(bytes.NewReader(raw))
	return res, nil
}

func signatureUnavailable(err error) bool {
	return errors.Is(err, ErrUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// IDTokenVerifier formats a KeySet error with %v, losing its typed cause. One
// call-local check retains that cause without relying on dependency error text.
type signatureCheck struct {
	keys oidc.KeySet
	err  error
}

func (s *signatureCheck) VerifySignature(ctx context.Context, raw string) ([]byte, error) {
	payload, err := s.keys.VerifySignature(ctx, raw)
	s.err = err
	return payload, err
}
