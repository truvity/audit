package writer

import (
	"context"
	"sync"
	"time"
)

// Dedupe remembers which records have been written.
//
// Every hop below the writer is at-least-once on purpose: the emitter's queue repeats
// what it could not confirm, the stream redelivers what was not acknowledged,
// and a retry after a timeout is the safe thing for a caller to do. This is
// where those repeats stop, and it is why none of them has to be careful.
//
// Asking and marking are two calls, and the order matters. The writer asks
// before it writes and marks only after the copies are durable. The other
// order — claim first, write second — reads better and is wrong for any store
// that outlives the process: a crash between the claim and the put would leave
// the identifier marked and the record nowhere, and the redelivery that would
// have saved it arrives looking like a duplicate. Marking afterwards can only
// go the other way, into a second copy of a record already written, and that
// costs an object: the index keeps one row per identifier and the digest chain
// accounts for both objects. A duplicate is a cost. A loss is a hole in an
// audit trail, and this component exists so that there are none.
type Dedupe interface {
	// Seen reports which of the identifiers have been written before. It does
	// not mark them.
	Seen(ctx context.Context, ids []string) (map[string]bool, error)
	// Mark records that the identifiers have been written. The writer calls it
	// once the copies are durable.
	Mark(ctx context.Context, ids []string) error
	// Purge forgets identifiers marked before a time.
	Purge(ctx context.Context, before time.Time) error
}

// MemoryDedupe remembers within one process.
//
// It is enough for the writer embedded in an application, where there is one
// writer and the queue covers a sink that was away, and it is not enough for a deployment
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
	now := d.now()
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if at, ok := d.seen[id]; ok && now.Sub(at) < d.window() {
			out[id] = true
		}
	}
	return out, nil
}

// Mark implements Dedupe.
func (d *MemoryDedupe) Mark(_ context.Context, ids []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen == nil {
		d.seen = map[string]time.Time{}
	}
	now := d.now()
	for _, id := range ids {
		if id != "" {
			d.seen[id] = now
		}
	}
	return nil
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
