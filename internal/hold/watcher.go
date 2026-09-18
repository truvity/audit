package hold

import (
	"context"
	"sync"
	"time"
)

// Watcher keeps the writer's view of which prefixes are held.
//
// A hold is placed on a prefix and the archive holds objects, so the sweep only
// covers what exists at the time. Everything written afterwards has to be held
// as it is written, and the only way the writer can do that is by knowing. It
// refreshes on an interval rather than being told, because the thing that
// places a hold is an operator at a terminal and the writer may not have been
// running when they did it.
type Watcher struct {
	Holds Store
	// Every is how often the holds are re-read. Default one minute. The
	// interval is the width of the window in which a new object under a
	// freshly held prefix is written without its hold, which is why it is
	// short and why `audit hold place` sweeps rather than trusting this.
	Every time.Duration

	mu     sync.RWMutex
	active []Record
	loaded bool
}

// Held reports whether a profile and tenant are under a hold.
//
// Until the first refresh has succeeded it answers false, which is why the
// writer refreshes once before it writes anything and refuses to start if that
// fails. Ready is for anything else that wants to know.
func (w *Watcher) Held(profile, tenant string) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	for _, r := range w.active {
		if r.Covers(profile, tenant) {
			return true
		}
	}
	return false
}

// Ready reports whether the holds have been read at least once.
func (w *Watcher) Ready() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.loaded
}

// Refresh re-reads the active holds.
func (w *Watcher) Refresh(ctx context.Context) error {
	active, err := w.Holds.Active(ctx)
	if err != nil {
		return err
	}
	w.mu.Lock()
	w.active, w.loaded = active, true
	w.mu.Unlock()
	return nil
}

// Run refreshes until the context is cancelled. onError is called for a
// refresh that failed; the previous answer stands until one succeeds, because
// forgetting a hold is worse than acting on a slightly old list.
func (w *Watcher) Run(ctx context.Context, onError func(error)) {
	every := w.Every
	if every <= 0 {
		every = time.Minute
	}
	if err := w.Refresh(ctx); err != nil && onError != nil {
		onError(err)
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.Refresh(ctx); err != nil && onError != nil {
				onError(err)
			}
		}
	}
}
