package httptransport

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
)

type secretReader struct {
	value string
	err   error
	calls atomic.Int64
	ref   domain.SecretRef
}

func (s *secretReader) ReadSecret(_ context.Context, ref domain.SecretRef) ([]byte, error) {
	s.calls.Add(1)
	s.ref = ref
	return []byte(s.value), s.err
}

type resolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (r resolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return r(ctx, network, host)
}

func sendRequest(raw string) ports.SendRequest {
	return ports.SendRequest{DeliveryID: "delivery_synthetic", Snapshot: domain.FrozenDelivery{
		Source:      domain.RevisionRef{ID: "source", Revision: 1},
		Transporter: domain.Transporter{ID: "receiver", Revision: 1, SourceID: "source", Kind: domain.TransporterHTTP, HTTP: domain.HTTPConfig{URL: raw, TimeoutSeconds: 1}},
		Body:        []byte(`{"message":"synthetic","contact":"***"}`),
	}}
}

func localTransport(t *testing.T, raw string, reader ports.SecretReader) *Transporter {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewPolicy([]OriginRule{{Origin: u.Scheme + "://" + u.Host, AllowPrivate: true}})
	if err != nil {
		t.Fatal(err)
	}
	transport, err := New(p, reader, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return transport
}

func TestSendFrozenBodyVersionedHeadersAndNoProxy(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"message":"synthetic","contact":"***"}` || r.Method != http.MethodPost || r.Header.Get("Idempotency-Key") != "delivery_synthetic" || r.Header.Get("Authorization") != "Bearer synthetic-v1" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("unexpected receiver request")
		}
		w.Header().Set("Retry-After", "9")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, strings.Repeat("synthetic receiver body", 5000))
	}))
	defer server.Close()
	// A proxy inherited from the service environment must never route deliveries.
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	reader := &secretReader{value: "Bearer synthetic-v1"}
	transport := localTransport(t, server.URL, reader)
	request := sendRequest(server.URL + "/synthetic")
	ref := domain.SecretRef{Name: "receiver-key", Version: "immutable-v1"}
	request.Snapshot.Transporter.HTTP.SecretHeaders = map[string]domain.SecretRef{"Authorization": ref}
	for range 2 {
		result, err := transport.Send(t.Context(), request)
		if err != nil || result.StatusCode != 429 || result.RetryAfter != 9*time.Second {
			t.Fatalf("send: %+v, %v", result, err)
		}
	}
	if calls.Load() != 2 || reader.calls.Load() != 2 || reader.ref != ref {
		t.Fatal("versioned secret or per-attempt sends changed")
	}
}

func TestRedirectNeverContactsTarget(t *testing.T) {
	var targetCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	result, err := localTransport(t, redirect.URL, &secretReader{}).Send(t.Context(), sendRequest(redirect.URL))
	if err != nil || result.StatusCode != 307 || targetCalls.Load() != 0 {
		t.Fatalf("redirect followed: %+v, %v, calls=%d", result, err, targetCalls.Load())
	}
}

func TestAcceptedHeadersReturnBeforeStalledBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Length", "1000000")
		w.WriteHeader(http.StatusAccepted)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	started := time.Now()
	result, err := localTransport(t, server.URL, &secretReader{}).Send(t.Context(), sendRequest(server.URL))
	if err != nil || result.StatusCode != 202 || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("known acceptance waited for receiver body: %+v, %v", result, err)
	}
}

func TestDNSValidationPinsConnectionAndPreservesTLSHostname(t *testing.T) {
	var received atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS.ServerName != "example.com" || !strings.HasPrefix(r.Host, "example.com:") {
			t.Error("TLS server name or Host was replaced by dial address")
		}
		received.Add(1)
		w.WriteHeader(204)
	}))
	defer server.Close()
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	origin := "https://example.com:" + port
	policy, err := NewPolicy([]OriginRule{{Origin: origin, AllowPrivate: true}})
	if err != nil {
		t.Fatal(err)
	}
	var lookups atomic.Int64
	resolver := resolverFunc(func(_ context.Context, network, host string) ([]netip.Addr, error) {
		if network != "ip" || host != "example.com" {
			t.Error("unexpected lookup")
		}
		if lookups.Add(1) == 1 {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("169.254.169.254")}, nil
	})
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	transport, err := New(policy, &secretReader{}, Options{Resolver: resolver, RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := transport.Send(t.Context(), sendRequest(origin)); err != nil || result.StatusCode != 204 {
		t.Fatalf("pinned TLS delivery: %+v, %v", result, err)
	}
	if _, err := transport.Send(t.Context(), sendRequest(origin)); !errors.Is(err, ports.ErrPolicyDenied) {
		t.Fatalf("rebound DNS not rejected: %v", err)
	}
	if received.Load() != 1 || lookups.Load() != 2 {
		t.Fatalf("connection was reused or DNS re-resolved during dial: receives=%d lookups=%d", received.Load(), lookups.Load())
	}
}

func TestUnapprovedOriginAndPrivateDNSMakeNoConnection(t *testing.T) {
	var received, lookups atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { received.Add(1) }))
	defer server.Close()
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	origin := "https://receiver.test:" + port
	policy, _ := NewPolicy([]OriginRule{{Origin: origin}})
	for _, addresses := range [][]netip.Addr{
		{netip.MustParseAddr("127.0.0.1")}, {netip.MustParseAddr("10.0.0.1")},
		{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("169.254.169.254")},
		{netip.MustParseAddr("100.64.0.1")}, {netip.MustParseAddr("::ffff:127.0.0.1")},
	} {
		transport, _ := New(policy, &secretReader{}, Options{Resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			lookups.Add(1)
			return addresses, nil
		})})
		if _, err := transport.Send(t.Context(), sendRequest(origin)); !errors.Is(err, ports.ErrPolicyDenied) {
			t.Errorf("unsafe DNS accepted: %v, %v", addresses, err)
		}
	}
	reader := &secretReader{}
	transport, _ := New(policy, reader, Options{Resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		lookups.Add(100)
		return nil, nil
	})})
	request := sendRequest("https://unapproved.test")
	request.Snapshot.Transporter.HTTP.SecretHeaders = map[string]domain.SecretRef{"Authorization": {Name: "synthetic", Version: "v1"}}
	if _, err := transport.Send(t.Context(), request); !errors.Is(err, ports.ErrPolicyDenied) || reader.calls.Load() != 0 {
		t.Fatalf("unapproved origin resolved secrets or sent: %v", err)
	}
	if lookups.Load() != 5 || received.Load() != 0 {
		t.Fatal("unapproved/unsafe destination caused a connection or extra lookup")
	}
}

func TestHeaderAttacksAndSecretsAreSafeFailures(t *testing.T) {
	var received atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { received.Add(1) }))
	defer server.Close()
	ref := domain.SecretRef{Name: "synthetic", Version: "v1"}
	for _, test := range []struct {
		name   string
		mutate func(*ports.SendRequest)
		secret string
	}{
		{"literal injection", func(r *ports.SendRequest) {
			r.Snapshot.Transporter.HTTP.Headers = map[string]string{"User-Agent": "ok\r\nInjected: yes"}
		}, "ok"},
		{"invalid name", func(r *ports.SendRequest) {
			r.Snapshot.Transporter.HTTP.SecretHeaders = map[string]domain.SecretRef{"X Bad": ref}
		}, "ok"},
		{"literal credentials", func(r *ports.SendRequest) {
			r.Snapshot.Transporter.HTTP.Headers = map[string]string{"Authorization": "synthetic"}
		}, "ok"},
		{"duplicate case", func(r *ports.SendRequest) {
			r.Snapshot.Transporter.HTTP.Headers = map[string]string{"Accept": "a", "accept": "b"}
		}, "ok"},
		{"cross-map duplicate", func(r *ports.SendRequest) {
			r.Snapshot.Transporter.HTTP.Headers = map[string]string{"Accept": "a"}
			r.Snapshot.Transporter.HTTP.SecretHeaders = map[string]domain.SecretRef{"accept": ref}
		}, "ok"},
		{"secret injection", func(r *ports.SendRequest) {
			r.Snapshot.Transporter.HTTP.SecretHeaders = map[string]domain.SecretRef{"Authorization": ref}
		}, "synthetic\nInjected: yes"},
		{"secret control", func(r *ports.SendRequest) {
			r.Snapshot.Transporter.HTTP.SecretHeaders = map[string]domain.SecretRef{"Authorization": ref}
		}, "synthetic\x00"},
		{"delivery ID injection", func(r *ports.SendRequest) { r.DeliveryID = "id\r\nInjected:yes" }, "ok"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := sendRequest(server.URL)
			test.mutate(&request)
			if _, err := localTransport(t, server.URL, &secretReader{value: test.secret}).Send(t.Context(), request); !errors.Is(err, ports.ErrInvalidPayload) || strings.Contains(err.Error(), "synthetic") {
				t.Fatalf("unsafe or revealing error: %v", err)
			}
		})
	}
	for _, key := range []string{"Host", "Content-Length", "Transfer-Encoding", "Connection", "Keep-Alive", "Trailer", "Upgrade", "TE", "Proxy-Authenticate", "Proxy-Authorization", "Proxy-Connection", "Content-Type", "Content-Encoding", "Idempotency-Key"} {
		request := sendRequest(server.URL)
		request.Snapshot.Transporter.HTTP.SecretHeaders = map[string]domain.SecretRef{key: ref}
		if _, err := localTransport(t, server.URL, &secretReader{value: "synthetic"}).Send(t.Context(), request); !errors.Is(err, ports.ErrInvalidPayload) {
			t.Errorf("reserved header %s accepted: %v", key, err)
		}
	}
	request := sendRequest(server.URL)
	request.Snapshot.Transporter.HTTP.SecretHeaders = map[string]domain.SecretRef{"Authorization": ref}
	if _, err := localTransport(t, server.URL, &secretReader{err: errors.New("synthetic secret value")}).Send(t.Context(), request); !errors.Is(err, ports.ErrSecretUnavailable) || strings.Contains(err.Error(), "synthetic") {
		t.Fatalf("secret error leaked: %v", err)
	}
	if received.Load() != 0 {
		t.Fatal("invalid headers reached receiver")
	}
}

func TestRetryAfterAndSafeNetworkErrors(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		value string
		want  time.Duration
	}{
		{"12", 12 * time.Second}, {"999999999999999999999999", MaxRetryAfter}, {"-1", 0}, {"+2", 0}, {"nonsense", 0},
		{now.Add(2 * time.Minute).Format(http.TimeFormat), 2 * time.Minute},
		{now.Add(24 * time.Hour).Format(http.TimeFormat), MaxRetryAfter}, {now.Add(-time.Second).Format(http.TimeFormat), 0},
	} {
		if got := retryAfter(test.value, now); got != test.want {
			t.Errorf("Retry-After %q: %v, want %v", test.value, got, test.want)
		}
	}
	policy, _ := NewPolicy([]OriginRule{{Origin: "https://receiver.test"}})
	transport, _ := New(policy, &secretReader{}, Options{Resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return nil, errors.New("synthetic credentials in DNS error")
	})})
	if _, err := transport.Send(t.Context(), sendRequest("https://receiver.test/private?synthetic=secret")); !errors.Is(err, ports.ErrNetwork) || strings.Contains(err.Error(), "synthetic") || strings.Contains(err.Error(), "receiver.test") {
		t.Fatalf("raw network error escaped: %v", err)
	}
}
