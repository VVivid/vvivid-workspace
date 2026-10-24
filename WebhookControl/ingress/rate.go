package ingress

import (
	"sync"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
)

const MaxTrackedSources = 1024

type sourceWindow struct {
	start time.Time
	count int
}

// Fixed source windows bound both request work and limiter memory. Unknown
// sources never allocate a window; full live capacity rejects new sources.
type sourceLimiter struct {
	mu      sync.Mutex
	maximum int
	windows map[domain.ID]sourceWindow
}

func (l *sourceLimiter) allow(source domain.ID, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	window, exists := l.windows[source]
	if !exists {
		if len(l.windows) == MaxTrackedSources {
			for id, tracked := range l.windows {
				if !now.Before(tracked.start.Add(time.Minute)) {
					delete(l.windows, id)
				}
			}
			if len(l.windows) == MaxTrackedSources {
				return false
			}
		}
		window.start = now
	}
	if !now.Before(window.start.Add(time.Minute)) {
		window = sourceWindow{start: now}
	}
	if window.count >= l.maximum {
		return false
	}
	window.count++
	l.windows[source] = window
	return true
}
