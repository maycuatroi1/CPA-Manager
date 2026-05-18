package httpapi

import (
	"context"
	"sync/atomic"
	"time"
)

// priorityGate implements a lightweight prioritization mechanism for
// concurrent /check requests. High-priority requests proceed without
// waiting; low-priority requests yield briefly if any high-priority
// requests are currently in flight.
type priorityGate struct {
	highInFlight int64
	maxWait      time.Duration
	pollInterval time.Duration
}

func newPriorityGate() *priorityGate {
	return &priorityGate{
		maxWait:      150 * time.Millisecond,
		pollInterval: 5 * time.Millisecond,
	}
}

// enter blocks (briefly) for low-priority callers when high-priority work
// is in flight. For high-priority callers it increments the in-flight
// counter and returns immediately. The returned release function must be
// called when the work completes.
func (pg *priorityGate) enter(ctx context.Context, highPriority bool) func() {
	if highPriority {
		atomic.AddInt64(&pg.highInFlight, 1)
		return func() {
			atomic.AddInt64(&pg.highInFlight, -1)
		}
	}

	deadline := time.Now().Add(pg.maxWait)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&pg.highInFlight) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return func() {}
		case <-time.After(pg.pollInterval):
		}
	}
	return func() {}
}
