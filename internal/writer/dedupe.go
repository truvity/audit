package writer

import (
	"context"
	"sync"
	"time"
)

// Dedupe remembers which records have been written.
//
// Every hop below the writer is at-least-once on purpose: the outbox repeats
// what it could not confirm, the stream redelivers what was not acknowledged,
// and a retry after a timeout is the safe thing for a caller to do. This is
// where those repeats stop, and it is why none of them has to be careful.
type Dedupe interface {
	// Seen marks the given identifiers and reports which had been seen before.
	Seen(ctx context.Context, ids []string) (map[string]bool, error)
	// Purge forgets identifiers marked before a time.
	Purge(ctx context.Context, before time.Time) error
}

// MemoryDedupe remembers within one process.
//
// It is enough for the writer embedded in an application, where there is one
// writer and the outbox covers restarts, and it is not enough for a deployment
// with several writers, where a shared table is the only thing that makes two
// replicas agree. A deployment gets what it configures, and the difference is
// documented rather than hidden behind an interface that pretends they are the
// same.
type MemoryDedupe struct {
	// Window is how long an identifier is remembered. Default 14 days.
	Window time.Duration
	// Now is the clock, for tests.
	Now func() time.Time

	mu   sync.Mutex
	seen map[string]time.Time
}

// Seen implements Dedupe.
func (d *MemoryDedupe) Seen(_ context.Context, ids []string) (map[string]bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen == nil {
		d.seen = map[string]time.Time{}
	}
	now := d.now()
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if at, ok := d.seen[id]; ok && now.Sub(at) < d.window() {
			out[id] = true
			continue
		}
		d.seen[id] = now
	}
	return out, nil
}

// Purge implements Dedupe.
func (d *MemoryDedupe) Purge(_ context.Context, before time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, at := range d.seen {
		if at.Before(before) {
			delete(d.seen, id)
		}
	}
	return nil
}

// Len is how many identifiers are remembered.
func (d *MemoryDedupe) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.seen)
}

func (d *MemoryDedupe) window() time.Duration {
	if d.Window > 0 {
		return d.Window
	}
	return 14 * 24 * time.Hour
}

func (d *MemoryDedupe) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}
