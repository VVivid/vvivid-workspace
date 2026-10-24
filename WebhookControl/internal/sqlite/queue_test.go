package sqlite

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
)

func TestConcurrentClaimsAcrossAdapters(t *testing.T) {
	s, clock, path, key := openTest(t)
	other, err := Open(path, key, WithClock(clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	request := requestAt(baseTime, "many", 0, 3600)
	for i := range 19 {
		delivery := request.Deliveries[0]
		delivery.ID = domain.ID(fmt.Sprintf("delivery-%02d", i))
		request.Deliveries = append(request.Deliveries, delivery)
	}
	mustAccept(t, s, request)
	results := make(chan ports.Lease, 40)
	failures := make(chan error, 40)
	var workers sync.WaitGroup
	for i := range 40 {
		workers.Go(func() {
			adapter := s
			if i%2 == 1 {
				adapter = other
			}
			lease, err := adapter.Claim(t.Context(), baseTime, time.Minute)
			if errors.Is(err, ports.ErrNoReadyDelivery) {
				return
			}
			if err != nil {
				failures <- err
				return
			}
			results <- lease
		})
	}
	workers.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	seen := make(map[domain.ID]bool)
	for lease := range results {
		if seen[lease.Delivery.ID] {
			t.Errorf("delivery concurrently claimed twice: %s", lease.Delivery.ID)
		}
		seen[lease.Delivery.ID] = true
		if lease.Delivery.Attempt != 1 || lease.Delivery.State != domain.DeliveryLeased || lease.Owner.Token == "" {
			t.Errorf("invalid first claim: %+v", lease)
		}
	}
	if len(seen) != len(request.Deliveries) {
		t.Fatalf("claimed %d of %d deliveries", len(seen), len(request.Deliveries))
	}
}

func TestConcurrentDuplicateAcceptance(t *testing.T) {
	s, clock, path, key := openTest(t)
	other, err := Open(path, key, WithClock(clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	first := requestAt(baseTime, "race-first", 0, 60)
	second := requestAt(baseTime, "race-second", 0, 60)
	second.Dedup = first.Dedup
	results := make(chan ports.Acceptance, 2)
	failures := make(chan error, 2)
	var workers sync.WaitGroup
	for i, request := range []ports.AcceptRequest{first, second} {
		workers.Go(func() {
			adapter := s
			if i == 1 {
				adapter = other
			}
			accepted, err := adapter.Accept(t.Context(), request)
			if err != nil {
				failures <- err
				return
			}
			results <- accepted
		})
	}
	workers.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var accepted []ports.Acceptance
	for result := range results {
		accepted = append(accepted, result)
	}
	if len(accepted) != 2 || accepted[0].EventID != accepted[1].EventID || !reflect.DeepEqual(accepted[0].DeliveryIDs, accepted[1].DeliveryIDs) || accepted[0].Duplicate == accepted[1].Duplicate {
		t.Fatalf("racing duplicate result: %+v", accepted)
	}
	if tableCount(t, s, "events") != 1 || tableCount(t, s, "deliveries") != 1 {
		t.Fatal("racing duplicate inserted extra records")
	}
}

func TestLeaseFencingRecoveryAndTerminalStates(t *testing.T) {
	s, clock, path, key := openTest(t)
	request := requestAt(baseTime, "recovery", 0, 120)
	mustAccept(t, s, request)
	first, err := s.Claim(t.Context(), baseTime, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, key, WithClock(clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.Claim(t.Context(), baseTime.Add(9*time.Second), time.Second); !errors.Is(err, ports.ErrNoReadyDelivery) {
		t.Fatalf("live lease reclaimed after restart: %v", err)
	}
	second, err := reopened.Claim(t.Context(), first.Until, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if second.Delivery.ID != first.Delivery.ID || second.Delivery.Attempt != 2 || second.Owner.Token == first.Owner.Token {
		t.Fatalf("recovered claim: %+v", second)
	}
	assertLost := func(t *testing.T, owner ports.LeaseToken, now time.Time) {
		t.Helper()
		if _, err := reopened.Renew(t.Context(), owner, now, time.Second); !errors.Is(err, ports.ErrLeaseLost) {
			t.Errorf("stale renew: %v", err)
		}
		if err := reopened.Complete(t.Context(), owner, now, 202); !errors.Is(err, ports.ErrLeaseLost) {
			t.Errorf("stale complete: %v", err)
		}
		if err := reopened.Retry(t.Context(), owner, now, now.Add(time.Second), "network_error"); !errors.Is(err, ports.ErrLeaseLost) {
			t.Errorf("stale retry: %v", err)
		}
		if err := reopened.Terminate(t.Context(), owner, now, "http_permanent"); !errors.Is(err, ports.ErrLeaseLost) {
			t.Errorf("stale terminate: %v", err)
		}
	}
	assertLost(t, first.Owner, first.Until)
	renewed, err := reopened.Renew(t.Context(), second.Owner, first.Until.Add(time.Second), 20*time.Second)
	if err != nil || !renewed.Equal(baseTime.Add(31*time.Second)) {
		t.Fatalf("renew: %v, %v", renewed, err)
	}
	if err := reopened.Complete(t.Context(), second.Owner, first.Until.Add(2*time.Second), 202); err != nil {
		t.Fatal(err)
	}
	assertLost(t, second.Owner, first.Until.Add(3*time.Second))
	if _, err := reopened.Claim(t.Context(), renewed, time.Second); !errors.Is(err, ports.ErrNoReadyDelivery) {
		t.Fatalf("completed delivery reclaimed: %v", err)
	}
	delivery, err := reopened.Delivery(t.Context(), second.Delivery.ID)
	if err != nil || delivery.State != domain.DeliveryHTTPAccepted || delivery.Attempt != 2 {
		t.Fatalf("completion status: %+v, %v", delivery, err)
	}
}

func TestRetryFailureAndWakeups(t *testing.T) {
	s, _, _, _ := openTest(t)
	request := requestAt(baseTime, "retry", 0, 60)
	mustAccept(t, s, request)
	select {
	case <-s.Wakeups():
	default:
		t.Fatal("commit did not signal workers")
	}
	lease, err := s.Claim(t.Context(), baseTime, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Retry(t.Context(), lease.Owner, baseTime.Add(time.Second), baseTime.Add(60*time.Second), "network_error"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("retry at payload expiry: %v", err)
	}
	next := baseTime.Add(5 * time.Second)
	if err := s.Retry(t.Context(), lease.Owner, baseTime.Add(time.Second), next, "network_error"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Wakeups():
	default:
		t.Fatal("retry commit did not signal")
	}
	if _, err := s.Claim(t.Context(), next.Add(-time.Nanosecond), time.Second); !errors.Is(err, ports.ErrNoReadyDelivery) {
		t.Fatalf("retry ran early: %v", err)
	}
	again, err := s.Claim(t.Context(), next, time.Second)
	if err != nil || again.Delivery.Attempt != 2 || again.Delivery.LastErrorCode != "network_error" || !reflect.DeepEqual(again.Delivery.Snapshot, request.Deliveries[0].Snapshot) {
		t.Fatalf("retry changed frozen job: %+v, %v", again, err)
	}
	if err := s.Terminate(t.Context(), again.Owner, next, "http_permanent"); err != nil {
		t.Fatal(err)
	}
	if delivery, err := s.Delivery(t.Context(), again.Delivery.ID); err != nil || delivery.State != domain.DeliveryFailed || delivery.LastErrorCode != "http_permanent" {
		t.Fatalf("failed delivery: %+v, %v", delivery, err)
	}
	if _, err := s.Claim(t.Context(), next.Add(time.Second), time.Second); !errors.Is(err, ports.ErrNoReadyDelivery) {
		t.Fatalf("failed delivery reclaimed: %v", err)
	}
	for i := range 10 {
		mustAccept(t, s, requestAt(baseTime, fmt.Sprintf("wake-%d", i), 0, 60))
	}
	if len(s.wakeups) != 1 {
		t.Fatalf("wakeups were not coalesced: %d", len(s.wakeups))
	}
}

func TestPayloadExpiryFencesActiveLease(t *testing.T) {
	s, clock, _, _ := openTest(t)
	request := requestAt(baseTime, "lease-expiry", 0, 2)
	mustAccept(t, s, request)
	lease, err := s.Claim(t.Context(), baseTime, time.Hour)
	if err != nil || !lease.Until.Equal(*request.Deliveries[0].ExpiresAt) {
		t.Fatalf("lease exceeded payload expiry: %+v, %v", lease, err)
	}
	clock.Set(lease.Until)
	if _, err := s.Renew(t.Context(), lease.Owner, clock.Now(), time.Second); !errors.Is(err, ports.ErrLeaseLost) {
		t.Fatalf("expired payload renewed: %v", err)
	}
	if err := s.Complete(t.Context(), lease.Owner, clock.Now(), 200); !errors.Is(err, ports.ErrLeaseLost) {
		t.Fatalf("expired payload completed: %v", err)
	}
	if err := s.Retry(t.Context(), lease.Owner, clock.Now(), clock.Now().Add(time.Second), "network_error"); !errors.Is(err, ports.ErrLeaseLost) {
		t.Fatalf("expired payload retried: %v", err)
	}
	if err := s.Terminate(t.Context(), lease.Owner, clock.Now(), "http_permanent"); !errors.Is(err, ports.ErrLeaseLost) {
		t.Fatalf("expired payload terminated: %v", err)
	}
	if _, err := s.Claim(t.Context(), clock.Now(), time.Second); !errors.Is(err, ports.ErrNoReadyDelivery) {
		t.Fatalf("expired payload reclaimed: %v", err)
	}
}

func TestInvalidQueueTimesDurationsAndErrorMessages(t *testing.T) {
	s, _, _, _ := openTest(t)
	mustAccept(t, s, requestAt(baseTime, "invalid-queue", 0, 60))
	for _, test := range []struct {
		now      time.Time
		duration time.Duration
	}{
		{baseTime, 0}, {baseTime, -time.Second}, {time.Time{}, time.Second},
		{time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC), time.Second},
		{time.Unix(0, math.MaxInt64), time.Second}, {baseTime, time.Duration(math.MaxInt64)},
	} {
		if _, err := s.Claim(t.Context(), test.now, test.duration); !errors.Is(err, ErrInvalid) {
			t.Errorf("unsafe claim accepted: %v", err)
		}
	}
	lease, err := s.Claim(t.Context(), baseTime, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Renew(t.Context(), lease.Owner, baseTime, -time.Second); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsafe renewal: %v", err)
	}
	if err := s.Complete(t.Context(), lease.Owner, baseTime, 500); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-2xx completed: %v", err)
	}
	if err := s.Retry(t.Context(), lease.Owner, baseTime, baseTime.Add(-time.Second), "network_error"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("past retry accepted: %v", err)
	}
	if err := s.Retry(t.Context(), lease.Owner, baseTime, baseTime.Add(time.Second), originalJSON); !errors.Is(err, ErrInvalid) {
		t.Fatalf("raw message accepted: %v", err)
	}
	if err := s.Terminate(t.Context(), lease.Owner, time.Time{}, "timeout"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero transition time: %v", err)
	}
	if err := s.Terminate(t.Context(), lease.Owner, baseTime, "receiver body with private data"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("receiver message accepted: %v", err)
	}
	if err := s.Terminate(t.Context(), lease.Owner, baseTime, "synthetic_secret_without_spaces"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("arbitrary machine-like message accepted: %v", err)
	}
	if err := s.Cleanup(t.Context(), time.Time{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero cleanup time: %v", err)
	}
	if err := s.Complete(t.Context(), lease.Owner, lease.Until, 200); !errors.Is(err, ports.ErrLeaseLost) {
		t.Fatalf("update at lease expiry: %v", err)
	}
}
