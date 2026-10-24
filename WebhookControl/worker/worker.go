// Package worker runs a bounded pool over durable leased deliveries. It knows
// only the queue, clock and single-attempt Transporter ports.
package worker

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
)

var (
	ErrOptions = errors.New("invalid worker options")
	ErrRunning = errors.New("worker already running")
	ErrQueue   = errors.New("delivery queue unavailable")
)

type Dependencies struct {
	Queue       ports.Queue
	Clock       ports.Clock
	Transporter ports.Transporter
}

// Zero values select defaults. Retry delays use exponential backoff with jitter
// in [half, full] of the capped delay; Retry-After can raise it to MaxBackoff.
type Options struct {
	Concurrency    int
	LeaseDuration  time.Duration
	RenewInterval  time.Duration
	PollInterval   time.Duration
	MaxAttempts    uint64
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

type Worker struct {
	dependencies Dependencies
	options      Options
	running      atomic.Bool
}

func New(dependencies Dependencies, options Options) (*Worker, error) {
	if dependencies.Queue == nil || dependencies.Clock == nil || dependencies.Transporter == nil {
		return nil, ErrOptions
	}
	if options.Concurrency == 0 {
		options.Concurrency = 4
	}
	if options.LeaseDuration == 0 {
		options.LeaseDuration = 30 * time.Second
	}
	if options.RenewInterval == 0 {
		options.RenewInterval = options.LeaseDuration / 3
	}
	if options.PollInterval == 0 {
		options.PollInterval = time.Second
	}
	if options.MaxAttempts == 0 {
		options.MaxAttempts = 8
	}
	if options.InitialBackoff == 0 {
		options.InitialBackoff = time.Second
	}
	if options.MaxBackoff == 0 {
		options.MaxBackoff = time.Minute
	}
	if options.Concurrency < 1 || options.Concurrency > 64 || options.LeaseDuration <= 0 || options.LeaseDuration > 5*time.Minute || options.RenewInterval <= 0 || options.RenewInterval >= options.LeaseDuration || options.PollInterval <= 0 || options.PollInterval > time.Minute || options.MaxAttempts > 100 || options.InitialBackoff <= 0 || options.MaxBackoff < options.InitialBackoff || options.MaxBackoff > time.Hour {
		return nil, ErrOptions
	}
	return &Worker{dependencies: dependencies, options: options}, nil
}

// Run scans immediately on startup and after each completed job. Wakeups reduce
// latency; polling recovers retries, lost signals and leases left by a crash.
// A queue failure stops the pool, leaving fenced leases recoverable on restart.
func (w *Worker) Run(ctx context.Context) error {
	if !w.running.CompareAndSwap(false, true) {
		return ErrRunning
	}
	defer w.running.Store(false)
	poolCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	failures := make(chan error, w.options.Concurrency)
	var tasks sync.WaitGroup
	for range w.options.Concurrency {
		tasks.Go(func() {
			if err := w.runLoop(poolCtx); err != nil {
				failures <- err
				cancel()
			}
		})
	}
	tasks.Wait()
	select {
	case err := <-failures:
		return err
	default:
		return ctx.Err()
	}
}

func (w *Worker) runLoop(ctx context.Context) error {
	wakeups := w.dependencies.Queue.Wakeups()
	for ctx.Err() == nil {
		now := w.dependencies.Clock.Now()
		lease, err := w.dependencies.Queue.Claim(ctx, now, w.options.LeaseDuration)
		if err == nil {
			if err := w.deliver(ctx, lease); err != nil {
				return err
			}
			continue
		}
		if ctx.Err() != nil {
			return nil
		}
		if !errors.Is(err, ports.ErrNoReadyDelivery) {
			return ErrQueue
		}
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-wakeups:
			if !ok {
				wakeups = nil
			}
		case <-w.dependencies.Clock.After(w.options.PollInterval):
		}
	}
	return nil
}

func (w *Worker) deliver(ctx context.Context, lease ports.Lease) error {
	now := w.dependencies.Clock.Now()
	if ctx.Err() != nil || !now.Before(lease.Until) || payloadExpired(lease, now) {
		return nil
	}
	if lease.Delivery.Attempt == 0 || lease.Owner.DeliveryID != lease.Delivery.ID || lease.Owner.Token == "" {
		return ErrQueue
	}
	if lease.Delivery.Attempt > w.options.MaxAttempts {
		return transitionError(ctx, w.dependencies.Queue.Terminate(ctx, lease.Owner, now, "retries_exhausted"))
	}
	result, attemptErr, leaseOwned := w.sendWithLease(ctx, lease)
	if !leaseOwned {
		return attemptErr
	}
	now = w.dependencies.Clock.Now()
	if ctx.Err() != nil || payloadExpired(lease, now) {
		return nil
	}
	if attemptErr == nil && result.StatusCode >= 200 && result.StatusCode <= 299 {
		return transitionError(ctx, w.dependencies.Queue.Complete(ctx, lease.Owner, now, result.StatusCode))
	}
	code, retry := classify(result, attemptErr)
	if !retry {
		return transitionError(ctx, w.dependencies.Queue.Terminate(ctx, lease.Owner, now, code))
	}
	if lease.Delivery.Attempt >= w.options.MaxAttempts {
		return transitionError(ctx, w.dependencies.Queue.Terminate(ctx, lease.Owner, now, "retries_exhausted"))
	}
	next := now.Add(w.retryDelay(lease.Delivery.Attempt, result.RetryAfter))
	if expiry := lease.Delivery.ExpiresAt; expiry != nil && !next.Before(*expiry) {
		return transitionError(ctx, w.dependencies.Queue.Terminate(ctx, lease.Owner, now, "retry_window_exhausted"))
	}
	return transitionError(ctx, w.dependencies.Queue.Retry(ctx, lease.Owner, now, next, code))
}

type sendOutcome struct {
	result ports.SendResult
	err    error
}

type renewal struct {
	until time.Time
	err   error
}

// The supervisor remains responsive to the old lease deadline while a renewal
// is in flight. Renewal failure, expiry and shutdown all cancel the HTTP request
// before this worker may claim another job.
//
// When leaseOwned is false, the caller must skip delivery transitions; err is
// nil for lease loss, expiry or cancellation and ErrQueue for an unexpected
// queue failure.
func (w *Worker) sendWithLease(ctx context.Context, lease ports.Lease) (result ports.SendResult, err error, leaseOwned bool) {
	attemptCtx, cancel := context.WithCancel(ctx)
	var tasks sync.WaitGroup
	completed := make(chan sendOutcome, 1)
	tasks.Go(func() {
		if attemptCtx.Err() != nil || !w.dependencies.Clock.Now().Before(lease.Until) || payloadExpired(lease, w.dependencies.Clock.Now()) {
			completed <- sendOutcome{err: context.Canceled}
			return
		}
		result, err := w.dependencies.Transporter.Send(attemptCtx, ports.SendRequest{DeliveryID: lease.Delivery.ID, Snapshot: lease.Delivery.Snapshot})
		completed <- sendOutcome{result: result, err: err}
	})
	until := lease.Until
	if expiry := lease.Delivery.ExpiresAt; expiry != nil && expiry.Before(until) {
		until = *expiry
	}
	deadline := w.dependencies.Clock.After(until.Sub(w.dependencies.Clock.Now()))
	heartbeat := w.dependencies.Clock.After(w.options.RenewInterval)
	renewed := make(chan renewal, 1)
	renewalPending := false
supervise:
	for {
		select {
		case <-ctx.Done():
			break supervise
		case <-deadline:
			break supervise
		case outcome := <-completed:
			result, err, leaseOwned = outcome.result, outcome.err, true
			break supervise
		case <-heartbeat:
			now := w.dependencies.Clock.Now()
			if !now.Before(until) || payloadExpired(lease, now) {
				break supervise
			}
			heartbeat = nil
			renewalPending = true
			tasks.Go(func() {
				newUntil, err := w.dependencies.Queue.Renew(attemptCtx, lease.Owner, now, w.options.LeaseDuration)
				renewed <- renewal{until: newUntil, err: err}
			})
		case renewal := <-renewed:
			renewalPending = false
			if failure := renewalQueueError(ctx, renewal.err, false); failure != nil {
				err = failure
				break supervise
			}
			now := w.dependencies.Clock.Now()
			if ctx.Err() != nil || renewal.err != nil || !now.Before(until) || !now.Before(renewal.until) || payloadExpired(lease, now) {
				break supervise
			}
			until = renewal.until
			if expiry := lease.Delivery.ExpiresAt; expiry != nil && expiry.Before(until) {
				until = *expiry
			}
			deadline = w.dependencies.Clock.After(until.Sub(now))
			heartbeat = w.dependencies.Clock.After(w.options.RenewInterval)
		}
	}
	// A send can finish while Renew is still running. Join and inspect that
	// operation before exposing the send outcome or permitting a transition.
	cancel()
	tasks.Wait()
	if renewalPending {
		renewal := <-renewed
		failure := renewalQueueError(ctx, renewal.err, true)
		switch {
		case failure != nil:
			err, leaseOwned = failure, false
		case errors.Is(renewal.err, ports.ErrLeaseLost):
			err, leaseOwned = nil, false
		case renewal.err == nil && w.dependencies.Clock.Now().Before(until):
			until = renewal.until
		}
	}
	if ctx.Err() != nil {
		return ports.SendResult{}, nil, false
	}
	if !leaseOwned {
		return ports.SendResult{}, err, false
	}
	now := w.dependencies.Clock.Now()
	if !now.Before(until) || payloadExpired(lease, now) {
		return ports.SendResult{}, nil, false
	}
	return result, err, true
}

// Classify renewal errors before deadline checks so expiry cannot hide a queue
// failure. Only cleanup intentionally cancels a renewal with a live parent;
// that explains context.Canceled, never an unrelated database error.
func renewalQueueError(parent context.Context, err error, canceledForCleanup bool) error {
	if parent.Err() != nil || err == nil || errors.Is(err, ports.ErrLeaseLost) || canceledForCleanup && errors.Is(err, context.Canceled) {
		return nil
	}
	return ErrQueue
}

func payloadExpired(lease ports.Lease, now time.Time) bool {
	return lease.Delivery.ExpiresAt != nil && !now.Before(*lease.Delivery.ExpiresAt)
}

func transitionError(ctx context.Context, err error) error {
	if err == nil || errors.Is(err, ports.ErrLeaseLost) || ctx.Err() != nil {
		return nil
	}
	return ErrQueue
}

func classify(result ports.SendResult, err error) (code string, retry bool) {
	switch {
	case errors.Is(err, ports.ErrPolicyDenied):
		return "policy_denied", false
	case errors.Is(err, ports.ErrSecretUnavailable):
		return "secret_unavailable", false
	case errors.Is(err, ports.ErrInvalidPayload):
		return "invalid_payload", false
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout", true
	case errors.Is(err, context.Canceled):
		return "canceled", true
	case err != nil:
		return "network_error", true
	case result.StatusCode == 408 || result.StatusCode == 429 || result.StatusCode >= 500 && result.StatusCode <= 599:
		return "http_retryable", true
	case result.StatusCode < 100 || result.StatusCode > 599:
		return "invalid_payload", false
	default:
		return "http_permanent", false
	}
}

func (w *Worker) retryDelay(attempt uint64, retryAfter time.Duration) time.Duration {
	delay := w.options.InitialBackoff
	for i := uint64(1); i < attempt && delay < w.options.MaxBackoff; i++ {
		delay = min(delay*2, w.options.MaxBackoff)
	}
	delay = max(time.Nanosecond, delay/2+time.Duration(rand.Int64N(int64(delay-delay/2)+1)))
	return max(delay, min(retryAfter, w.options.MaxBackoff))
}
