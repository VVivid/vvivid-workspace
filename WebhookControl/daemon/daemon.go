package daemon

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/clock"
	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/httptransport"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ingress"
	"github.com/VVivid/vvivid-workspace/WebhookControl/internal/keyfile"
	"github.com/VVivid/vvivid-workspace/WebhookControl/internal/sqlite"
	"github.com/VVivid/vvivid-workspace/WebhookControl/management"
	"github.com/VVivid/vvivid-workspace/WebhookControl/managementhttp"
	"github.com/VVivid/vvivid-workspace/WebhookControl/worker"
)

var ErrRuntime = errors.New("server stopped because a required component failed")

// Run owns SQLite, issuer verification, ingress, management, workers and cleanup.
// A required component's unexpected failure closes serving and cancels requests;
// normal cancellation first drains HTTP then cancels and joins background work.
func Run(ctx context.Context, supplied Config) error {
	c, err := supplied.Validated()
	if err != nil {
		return err
	}
	key, err := keyfile.Load(c.KeyFile)
	if err != nil {
		return err
	}
	defer clear(key)
	digestKey, err := keyfile.Load(c.DigestKeyFile)
	if err != nil {
		return err
	}
	defer clear(digestKey)
	if hmac.Equal(key, digestKey) {
		return errors.New("database and ingress digest keys must be different")
	}
	verifier, err := managementhttp.NewTokenVerifier(ctx, c.OAuth)
	if err != nil {
		return err
	}
	store, err := sqlite.Open(c.DatabasePath, key,
		sqlite.WithRetention(time.Duration(c.MetadataRetentionSeconds)*time.Second, time.Duration(c.DeduplicationRetentionSeconds)*time.Second),
		sqlite.WithMaxPageCount(c.MaxDatabasePages),
	)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.BindDigestKey(ctx, digestKey); err != nil {
		return err
	}
	if err := store.Cleanup(ctx, time.Now()); err != nil {
		return err
	}
	policy, err := httptransport.NewPolicy(c.DestinationOrigins)
	if err != nil {
		return err
	}
	transporter, err := httptransport.New(policy, store.SecretsFor(domain.SecretHTTPHeader), httptransport.Options{})
	if err != nil {
		return err
	}
	observations := &counters{sources: map[domain.ID]domain.IngressCounters{}}
	hooks, err := ingress.New(ingress.Dependencies{
		Configurations: store,
		Secrets:        store.SecretsFor(domain.SecretSourceAuth),
		Storage:        store,
		Policy:         policy,
		Clock:          clock.Real{},
		Transporter:    transporter,
	}, ingress.Options{
		DigestKey:           digestKey,
		DeduplicationWindow: time.Duration(c.DeduplicationRetentionSeconds) * time.Second,
		RequestsPerMinute:   c.RequestsPerMinute,
		ImmediateBudget:     time.Duration(c.ImmediateBudgetSeconds) * time.Second,
		Observe:             observations.observe,
	})
	if err != nil {
		return err
	}
	service, err := management.New(store, policy, time.Now, management.WithIngressCounters(observations.read))
	if err != nil {
		return err
	}
	managementHandler, err := managementhttp.NewHandler(managementhttp.Config{OAuth: c.OAuth, AllowedOrigins: c.AllowedManagementOrigins}, service, verifier)
	if err != nil {
		return err
	}
	pool, err := worker.New(worker.Dependencies{Queue: store, Clock: clock.Real{}, Transporter: transporter}, c.workerOptions())
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return errors.New("cannot listen on configured address")
	}
	defer listener.Close()
	// Detach parent cancellation until graceful HTTP draining completes. Both
	// workers and accepted request contexts are explicitly canceled below.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	var ready atomic.Bool
	mux := http.NewServeMux()
	mux.Handle("/v1/hooks/", hooks)
	mux.Handle("/", managementHandler)
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) { statusJSON(w, http.StatusOK, "ok") })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		checkCtx, stop := context.WithTimeout(r.Context(), time.Second)
		defer stop()
		if !ready.Load() || store.Ping(checkCtx) != nil {
			statusJSON(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		statusJSON(w, http.StatusOK, "ok")
	})
	capacity := make(chan struct{}, c.MaxInFlightRequests)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !ready.Load() {
			statusJSON(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		select {
		case capacity <- struct{}{}:
			defer func() { <-capacity }()
		default:
			statusJSON(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		requestCtx, stop := context.WithTimeout(r.Context(), time.Duration(c.RequestTimeoutSeconds)*time.Second)
		defer stop()
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		mux.ServeHTTP(w, r.WithContext(requestCtx))
	})
	server := &http.Server{
		Handler:           handler,
		BaseContext:       func(net.Listener) context.Context { return runCtx },
		ReadHeaderTimeout: time.Duration(c.ReadHeaderTimeoutSeconds) * time.Second,
		ReadTimeout:       time.Duration(c.ReadTimeoutSeconds) * time.Second,
		WriteTimeout:      time.Duration(c.WriteTimeoutSeconds) * time.Second,
		IdleTimeout:       time.Duration(c.IdleTimeoutSeconds) * time.Second,
		MaxHeaderBytes:    c.MaxHeaderBytes,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	failures := make(chan error, 3)
	var tasks sync.WaitGroup
	fail := func(err error) {
		// Close the acceptance gate before reporting the failure. Canceling the
		// base context stops in-flight database acceptance and outbound sends.
		ready.Store(false)
		cancel()
		server.Close()
		failures <- err
	}
	ready.Store(true)
	tasks.Go(func() {
		if err := pool.Run(runCtx); runCtx.Err() == nil {
			fail(ErrRuntime)
		} else if err != nil && !errors.Is(err, context.Canceled) {
			failures <- ErrRuntime
		}
	})
	tasks.Go(func() {
		if err := cleanup(runCtx, store, time.Duration(c.CleanupIntervalSeconds)*time.Second); err != nil {
			fail(ErrRuntime)
		}
	})
	// A Ping plus startup cleanup established storage availability. Workers scan
	// persisted work immediately; any queue failure atomically clears this gate.
	tasks.Go(func() {
		if err := server.Serve(&limitedListener{Listener: listener, slots: make(chan struct{}, c.MaxConnections)}); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fail(ErrRuntime)
		}
	})
	var result error
	select {
	case <-ctx.Done():
	case result = <-failures:
	}
	ready.Store(false)
	drainCtx, stop := context.WithTimeout(context.Background(), time.Duration(c.ShutdownTimeoutSeconds)*time.Second)
	err = server.Shutdown(drainCtx)
	stop()
	cancel()
	server.Close()
	tasks.Wait()
	select {
	case failure := <-failures:
		result = failure
	default:
	}
	if result != nil {
		return result
	}
	if err != nil {
		return errors.New("HTTP shutdown deadline exceeded")
	}
	return nil
}

func cleanup(ctx context.Context, store *sqlite.Store, interval time.Duration) error {
	ticks := time.Tick(interval)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticks:
			if err := store.Cleanup(ctx, time.Now()); err != nil && ctx.Err() == nil {
				return ErrRuntime
			}
		}
	}
}

func statusJSON(w http.ResponseWriter, status int, value string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(struct {
		Status string `json:"status"`
	}{value})
}

type limitedListener struct {
	net.Listener
	slots chan struct{}
}

func (l *limitedListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			return &limitedConn{Conn: conn, release: func() { <-l.slots }}, nil
		default:
			conn.Close()
		}
	}
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error { err := c.Conn.Close(); c.once.Do(c.release); return err }
