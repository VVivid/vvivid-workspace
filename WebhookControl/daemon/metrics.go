package daemon

import (
	"sync"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ingress"
)

// Only known configured sources enter this bounded map. A source beyond the
// bound has omitted counters; retained SQLite metrics remain available.
type counters struct {
	mu      sync.Mutex
	sources map[domain.ID]domain.IngressCounters
}

func (c *counters) observe(source domain.ID, accepted, duplicate bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, exists := c.sources[source]
	if !exists && len(c.sources) >= ingress.MaxTrackedSources {
		return
	}
	switch {
	case duplicate:
		v.DuplicateRequests++
	case accepted:
		v.AcceptedEvents++
	default:
		v.RejectedRequests++
	}
	c.sources[source] = v
}

func (c *counters) read(source domain.ID) *domain.IngressCounters {
	c.mu.Lock()
	defer c.mu.Unlock()
	if value, ok := c.sources[source]; ok {
		return new(value)
	}
	return nil
}
