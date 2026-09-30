package natssink

import (
	"testing"
	"time"
)

// The pause between failed fetches doubles from a tenth of a second to five,
// stays there, and starts over after a fetch that succeeds.
func TestTheFetchBackoffDoublesToItsCapAndResets(t *testing.T) {
	b := backoff{start: fetchRetryStart, limit: fetchRetryCap}
	want := []time.Duration{
		100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond,
		800 * time.Millisecond, 1600 * time.Millisecond, 3200 * time.Millisecond,
		5 * time.Second, 5 * time.Second,
	}
	for i, w := range want {
		if got := b.next(); got != w {
			t.Fatalf("pause %d = %s, want %s", i, got, w)
		}
	}
	b.reset()
	if got := b.next(); got != 100*time.Millisecond {
		t.Fatalf("after a success the pause is %s, want it to start over at 100ms", got)
	}
}
