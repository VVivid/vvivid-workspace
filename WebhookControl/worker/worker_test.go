package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/clock"
	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
)

type transporterFunc func(context.Context, ports.SendRequest) (ports.SendResult, error)

func (f transporterFunc) Send(ctx context.Context, request ports.SendRequest) (ports.SendResult, error) {
	return f(ctx, request)
}

type queueStub struct {
	claim func(context.Context, time.Time, time.Duration) (ports.Lease, error)
	renew func(context.Context, ports.LeaseToken, time.Time, time.Duration) (time.Time, error)
}

func (q queueStub) Claim(ctx context.Context, now time.Time, d time.Duration) (ports.Lease, error) {
	return q.claim(ctx, now, d)
}
func (q queueStub) Renew(ctx context.Context, token ports.LeaseToken, now time.Time, d time.Duration) (time.Time, error) {
	return q.renew(ctx, token, now, d)
}
func (queueStub) Complete(context.Context, ports.LeaseToken, time.Time, int) error { return nil }
func (queueStub) Retry(context.Context, ports.LeaseToken, time.Time, time.Time, string) error {
	return nil
}
func (queueStub) Terminate(context.Context, ports.LeaseToken, time.Time, string) error { return nil }
func (queueStub) Wakeups() <-chan struct{}                                             { return nil }

func TestFailureClassificationAndBoundedBackoff(t *testing.T) {
	for _, test := range []struct {
		status int
		err    error
		code   string
		retry  bool
	}{
		{0, ports.ErrPolicyDenied, "policy_denied", false}, {0, ports.ErrInvalidPayload, "invalid_payload", false},
		{0, ports.ErrSecretUnavailable, "secret_unavailable", false}, {0, context.DeadlineExceeded, "timeout", true},
		{0, ports.ErrNetwork, "network_error", true}, {0, errors.New("synthetic private receiver body"), "network_error", true},
		{408, nil, "http_retryable", true}, {429, nil, "http_retryable", true}, {503, nil, "http_retryable", true},
		{400, nil, "http_permanent", false}, {401, nil, "http_permanent", false}, {404, nil, "http_permanent", false},
		{301, nil, "http_permanent", false}, {307, nil, "http_permanent", false}, {0, nil, "invalid_payload", false},
	} {
		code, retry := classify(ports.SendResult{StatusCode: test.status}, test.err)
		if code != test.code || retry != test.retry {
			t.Errorf("classification status=%d: %s, %v", test.status, code, retry)
		}
	}
	w := &Worker{options: Options{InitialBackoff: time.Second, MaxBackoff: 8 * time.Second}}
	for attempt := uint64(1); attempt < 100; attempt++ {
		for range 20 {
			got := w.retryDelay(attempt, 0)
			full := time.Second << min(attempt-1, 3)
			if got < full/2 || got > full {
				t.Fatalf("backoff outside jitter bounds: attempt=%d delay=%v", attempt, got)
			}
		}
		if got := w.retryDelay(attempt, 24*time.Hour); got != 8*time.Second {
			t.Fatalf("Retry-After not capped: %v", got)
		}
	}
	w.options = Options{InitialBackoff: time.Nanosecond, MaxBackoff: time.Nanosecond}
	for range 100 {
		if delay := w.retryDelay(1, 0); delay != time.Nanosecond {
			t.Fatalf("smallest retry delay can schedule now: %v", delay)
		}
	}
}

func TestPoolBoundsConcurrencyAndCancelsOnShutdown(t *testing.T) {
	var claims, active, peak atomic.Int64
	queue := queueStub{
		claim: func(_ context.Context, now time.Time, d time.Duration) (ports.Lease, error) {
			claims.Add(1)
			return ports.Lease{Delivery: domain.Delivery{ID: "synthetic", Attempt: 1}, Owner: ports.LeaseToken{DeliveryID: "synthetic", Token: "token"}, Until: now.Add(d)}, nil
		},
		renew: func(_ context.Context, _ ports.LeaseToken, now time.Time, d time.Duration) (time.Time, error) {
			return now.Add(d), nil
		},
	}
	transport := transporterFunc(func(ctx context.Context, _ ports.SendRequest) (ports.SendResult, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for n > peak.Load() {
			previous := peak.Load()
			if previous >= n || peak.CompareAndSwap(previous, n) {
				break
			}
		}
		<-ctx.Done()
		return ports.SendResult{}, ctx.Err()
	})
	w, err := New(Dependencies{Queue: queue, Clock: clock.Real{}, Transporter: transport}, Options{Concurrency: 3})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	await(t, func() bool { return active.Load() == 3 })
	if err := w.Run(ctx); !errors.Is(err, ErrRunning) {
		t.Fatalf("same pool can run concurrently: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || active.Load() != 0 || peak.Load() != 3 || claims.Load() != 3 {
			t.Fatalf("pool bounds or shutdown: error=%v active=%d peak=%d claims=%d", err, active.Load(), peak.Load(), claims.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel active sends")
	}
}

func TestLeaseDeadlineCancelsWhileRenewalIsBlocked(t *testing.T) {
	var sendCanceled, renewalCanceled atomic.Bool
	queue := queueStub{renew: func(ctx context.Context, _ ports.LeaseToken, _ time.Time, _ time.Duration) (time.Time, error) {
		<-ctx.Done()
		renewalCanceled.Store(true)
		return time.Time{}, ctx.Err()
	}}
	transport := transporterFunc(func(ctx context.Context, _ ports.SendRequest) (ports.SendResult, error) {
		<-ctx.Done()
		sendCanceled.Store(true)
		return ports.SendResult{}, ctx.Err()
	})
	w, err := New(Dependencies{Queue: queue, Clock: clock.Real{}, Transporter: transport}, Options{LeaseDuration: 50 * time.Millisecond, RenewInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	lease := ports.Lease{Delivery: domain.Delivery{ID: "synthetic", Attempt: 1}, Owner: ports.LeaseToken{DeliveryID: "synthetic", Token: "token"}, Until: time.Now().Add(50 * time.Millisecond)}
	started := time.Now()
	_, _, owned := w.sendWithLease(t.Context(), lease)
	if owned || !sendCanceled.Load() || !renewalCanceled.Load() || time.Since(started) > 500*time.Millisecond {
		t.Fatal("blocked renewal prevented cancellation at the lease deadline")
	}
}

func TestRenewalStorageFailureStopsPoolWithoutFurtherClaims(t *testing.T) {
	var claims, sends, canceled atomic.Int64
	allSending := make(chan struct{})
	queue := queueStub{
		claim: func(_ context.Context, now time.Time, duration time.Duration) (ports.Lease, error) {
			claim := claims.Add(1)
			if claim > 2 {
				return ports.Lease{}, ports.ErrNoReadyDelivery
			}
			id := domain.ID("first")
			if claim == 2 {
				id = "second"
			}
			return ports.Lease{Delivery: domain.Delivery{ID: id, Attempt: 1}, Owner: ports.LeaseToken{DeliveryID: id, Token: "token"}, Until: now.Add(duration)}, nil
		},
		renew: func(ctx context.Context, owner ports.LeaseToken, _ time.Time, _ time.Duration) (time.Time, error) {
			if owner.DeliveryID == "first" {
				select {
				case <-allSending:
					return time.Time{}, errors.New("synthetic private database details")
				case <-ctx.Done():
					return time.Time{}, ctx.Err()
				}
			}
			<-ctx.Done()
			return time.Time{}, ctx.Err()
		},
	}
	transport := transporterFunc(func(ctx context.Context, _ ports.SendRequest) (ports.SendResult, error) {
		if sends.Add(1) == 2 {
			close(allSending)
		}
		<-ctx.Done()
		canceled.Add(1)
		return ports.SendResult{}, ctx.Err()
	})
	w, err := New(Dependencies{Queue: queue, Clock: clock.Real{}, Transporter: transport}, Options{Concurrency: 2, LeaseDuration: time.Second, RenewInterval: 5 * time.Millisecond, PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err = w.Run(ctx)
	if !errors.Is(err, ErrQueue) || err.Error() != ErrQueue.Error() || claims.Load() != 2 || sends.Load() != 2 || canceled.Load() != 2 {
		t.Fatalf("renewal failure did not stop the pool safely: error=%v claims=%d sends=%d canceled=%d", err, claims.Load(), sends.Load(), canceled.Load())
	}
}

func TestSendCompletionReconcilesRenewalFailureBeforeTransition(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
	}{{name: "accepted", status: 204}, {name: "retryable", status: 503}} {
		t.Run(test.name, func(t *testing.T) {
			var claims, sends, siblingCanceled atomic.Int64
			allSending := make(chan struct{})
			renewalStarted := make(chan struct{})
			queue := &trackingQueue{Queue: queueStub{
				claim: func(_ context.Context, now time.Time, duration time.Duration) (ports.Lease, error) {
					claim := claims.Add(1)
					if claim > 2 {
						return ports.Lease{}, ports.ErrNoReadyDelivery
					}
					id := domain.ID("first")
					if claim == 2 {
						id = "second"
					}
					return ports.Lease{Delivery: domain.Delivery{ID: id, Attempt: 1}, Owner: ports.LeaseToken{DeliveryID: id, Token: "token"}, Until: now.Add(duration)}, nil
				},
				renew: func(ctx context.Context, owner ports.LeaseToken, _ time.Time, _ time.Duration) (time.Time, error) {
					if owner.DeliveryID == "first" {
						select {
						case <-allSending:
							close(renewalStarted)
						case <-ctx.Done():
							return time.Time{}, ctx.Err()
						}
						// Send will now finish first. Report the database failure only
						// after sendWithLease cancels this renewal during its join.
						<-ctx.Done()
						return time.Time{}, errors.New("synthetic private database details")
					}
					<-ctx.Done()
					return time.Time{}, ctx.Err()
				},
			}}
			transport := transporterFunc(func(ctx context.Context, request ports.SendRequest) (ports.SendResult, error) {
				if sends.Add(1) == 2 {
					close(allSending)
				}
				if request.DeliveryID == "first" {
					select {
					case <-renewalStarted:
						return ports.SendResult{StatusCode: test.status}, nil
					case <-ctx.Done():
						return ports.SendResult{}, ctx.Err()
					}
				}
				<-ctx.Done()
				siblingCanceled.Add(1)
				return ports.SendResult{}, ctx.Err()
			})
			w, err := New(Dependencies{Queue: queue, Clock: clock.Real{}, Transporter: transport}, Options{Concurrency: 2, LeaseDuration: time.Second, RenewInterval: 5 * time.Millisecond, PollInterval: 5 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			err = w.Run(ctx)
			if !errors.Is(err, ErrQueue) || err.Error() != ErrQueue.Error() || claims.Load() != 2 || queue.transitions.Load() != 0 || siblingCanceled.Load() != 1 {
				t.Fatalf("racing renewal failure was lost: error=%v claims=%d transitions=%d sibling_canceled=%d", err, claims.Load(), queue.transitions.Load(), siblingCanceled.Load())
			}
		})
	}
}

type renewalOrderingClock struct {
	adjustableClock
	heartbeatInterval time.Duration
	heartbeat         <-chan time.Time
}

func (c *renewalOrderingClock) After(duration time.Duration) <-chan time.Time {
	if duration == c.heartbeatInterval {
		return c.heartbeat
	}
	// Hold deadline signals back even after Now advances. This forces the
	// supervisor to consume the renewal result before its deadline case.
	return nil
}

func TestConsumedRenewalFailureAfterDeadlineStopsPool(t *testing.T) {
	for _, payloadExpiry := range []bool{false, true} {
		name := "lease deadline"
		if payloadExpiry {
			name = "payload expiry"
		}
		t.Run(name, func(t *testing.T) {
			base := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
			heartbeat := make(chan time.Time, 1)
			heartbeat <- base
			testClock := &renewalOrderingClock{heartbeatInterval: time.Millisecond, heartbeat: heartbeat}
			testClock.nanos.Store(base.UnixNano())
			var claims atomic.Int64
			var sendCanceled atomic.Bool
			sendStarted := make(chan struct{})
			lease := ports.Lease{Delivery: domain.Delivery{ID: "synthetic", Attempt: 1}, Owner: ports.LeaseToken{DeliveryID: "synthetic", Token: "token"}, Until: base.Add(time.Minute)}
			if payloadExpiry {
				lease.Delivery.ExpiresAt = new(base.Add(10 * time.Second))
			}
			queue := &trackingQueue{Queue: queueStub{
				claim: func(context.Context, time.Time, time.Duration) (ports.Lease, error) {
					if claims.Add(1) != 1 {
						return ports.Lease{}, ports.ErrNoReadyDelivery
					}
					return lease, nil
				},
				renew: func(ctx context.Context, _ ports.LeaseToken, _ time.Time, _ time.Duration) (time.Time, error) {
					select {
					case <-sendStarted:
					case <-ctx.Done():
						return time.Time{}, ctx.Err()
					}
					advance := 2 * time.Minute
					if payloadExpiry {
						advance = 20 * time.Second
					}
					testClock.nanos.Store(base.Add(advance).UnixNano())
					return time.Time{}, errors.New("synthetic private database details")
				},
			}}
			transport := transporterFunc(func(ctx context.Context, _ ports.SendRequest) (ports.SendResult, error) {
				close(sendStarted)
				<-ctx.Done()
				sendCanceled.Store(true)
				return ports.SendResult{}, ctx.Err()
			})
			w, err := New(Dependencies{Queue: queue, Clock: testClock, Transporter: transport}, Options{Concurrency: 1, LeaseDuration: time.Minute, RenewInterval: time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			err = w.Run(ctx)
			if !errors.Is(err, ErrQueue) || err.Error() != ErrQueue.Error() || claims.Load() != 1 || queue.transitions.Load() != 0 || !sendCanceled.Load() {
				t.Fatalf("expired deadline hid consumed renewal failure: error=%v claims=%d transitions=%d canceled=%v", err, claims.Load(), queue.transitions.Load(), sendCanceled.Load())
			}
		})
	}
}

func TestSendCompletionPreservesExpectedRenewalOutcomes(t *testing.T) {
	for _, test := range []struct {
		name         string
		err          error
		owned        bool
		cancelParent bool
	}{
		{name: "cleanup cancellation", err: context.Canceled, owned: true},
		{name: "parent cancellation", err: context.Canceled, cancelParent: true},
		{name: "lost lease", err: ports.ErrLeaseLost, owned: false},
		{name: "successful renewal", owned: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			renewalStarted := make(chan struct{})
			queue := queueStub{renew: func(ctx context.Context, _ ports.LeaseToken, now time.Time, duration time.Duration) (time.Time, error) {
				close(renewalStarted)
				<-ctx.Done()
				return now.Add(duration), test.err
			}}
			transport := transporterFunc(func(ctx context.Context, _ ports.SendRequest) (ports.SendResult, error) {
				select {
				case <-renewalStarted:
					if test.cancelParent {
						cancel()
					}
					return ports.SendResult{StatusCode: 204}, nil
				case <-ctx.Done():
					return ports.SendResult{}, ctx.Err()
				}
			})
			w, err := New(Dependencies{Queue: queue, Clock: clock.Real{}, Transporter: transport}, Options{LeaseDuration: time.Second, RenewInterval: 5 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			lease := ports.Lease{Delivery: domain.Delivery{ID: "synthetic", Attempt: 1}, Owner: ports.LeaseToken{DeliveryID: "synthetic", Token: "token"}, Until: time.Now().Add(time.Second)}
			result, err, owned := w.sendWithLease(ctx, lease)
			if err != nil || owned != test.owned || owned && result.StatusCode != 204 || !owned && result.StatusCode != 0 {
				t.Fatalf("expected renewal outcome changed: status=%d error=%v owned=%v", result.StatusCode, err, owned)
			}
		})
	}
}

func TestRetryWindowDoesNotScheduleAtOrBeyondExpiry(t *testing.T) {
	store, _, _ := openStore(t, clock.Real{})
	t.Cleanup(func() { store.Close() })
	// No network is used in this case; an approved loopback origin supplies only
	// the frozen configuration required by normal ingress acceptance.
	policy, _ := receiverTransport(t, "http://127.0.0.1:1")
	accepted := enqueueHTTP(t, store, policy, clock.Real{}, []string{"http://127.0.0.1:1"}, 1, 1)
	transport := transporterFunc(func(context.Context, ports.SendRequest) (ports.SendResult, error) {
		return ports.SendResult{StatusCode: 429, RetryAfter: time.Hour}, nil
	})
	startWorker(t, store, clock.Real{}, transport, Options{Concurrency: 1, PollInterval: 5 * time.Millisecond, InitialBackoff: 2 * time.Second, MaxBackoff: 2 * time.Second})
	await(t, func() bool { return deliveryState(t, store, accepted.DeliveryIDs[0]).State == domain.DeliveryFailed })
	got := deliveryState(t, store, accepted.DeliveryIDs[0])
	if got.Attempt != 1 || got.LastErrorCode != "retry_window_exhausted" {
		t.Fatalf("retry window: attempts=%d error=%s", got.Attempt, got.LastErrorCode)
	}
}
