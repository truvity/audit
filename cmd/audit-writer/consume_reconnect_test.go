package main

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvity/audit/sdk/sink"
)

// tally is a target that remembers how many times each record was handed to
// it, and runs a hook as each batch arrives, before the consumer acknowledges
// it.
type tally struct {
	mu      sync.Mutex
	seen    map[string]int
	writes  int
	handled int
	onCall  func(call int)
}

func (c *tally) Write(_ context.Context, req *sink.Request) (*sink.Result, error) {
	c.mu.Lock()
	if c.seen == nil {
		c.seen = map[string]int{}
	}
	for _, r := range req.Records {
		c.seen[r.GetId()]++
	}
	c.writes++
	call := c.writes
	c.mu.Unlock()
	if c.onCall != nil {
		c.onCall(call)
	}
	c.mu.Lock()
	c.handled += len(req.Records)
	c.mu.Unlock()
	return &sink.Result{Accepted: len(req.Records)}, nil
}

// done is how many records have been through the writer and its hook: the
// batch in hand has been seen by the test when this counts it.
func (c *tally) done() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.handled
}

func (c *tally) repeated() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for id, n := range c.seen {
		if n != 1 {
			out = append(out, id)
		}
	}
	return out
}

// errorsLogged collects what the process logs at ERROR through slog's default,
// which is where the consumer reports.
type errorsLogged struct {
	slog.Handler
	mu   sync.Mutex
	msgs []string
}

func (e *errorsLogged) Enabled(context.Context, slog.Level) bool { return true }
func (e *errorsLogged) Handle(_ context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError {
		e.mu.Lock()
		e.msgs = append(e.msgs, r.Message)
		e.mu.Unlock()
	}
	return nil
}
func (e *errorsLogged) WithAttrs([]slog.Attr) slog.Handler { return e }
func (e *errorsLogged) WithGroup(string) slog.Handler      { return e }

func (e *errorsLogged) consumer() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, m := range e.msgs {
		// Other tests' connections winding down log through the default too;
		// only what this consumer says is this test's concern.
		if strings.HasPrefix(m, "the writer refused") || strings.HasPrefix(m, "the stream consumer stopped") {
			out = append(out, m)
		}
	}
	return out
}

// The consumer across the planned reconnect. The token lease reopens the
// connection while the consumer is in the middle of its work, and every record
// is written once, not twice and not never, and nothing is logged at ERROR.
//
// The reconnect is fired from the target, after a batch has been taken and
// before its messages are acknowledged, so the acknowledgements are sent into
// a connection that is reconnecting; and again once the stream is drained, when
// the consumer is parked in a fetch that the reconnect cuts off. The lease's
// clock and timer are the test's, so none of it waits on a scheduler.
//
// The ack wait is long on purpose: a record lost to the reconnect would only
// come back after it, and the test would say so rather than hide it behind a
// redelivery.
func TestAPlannedReconnectUnderTheConsumerWritesEveryRecordOnce(t *testing.T) {
	out := &logs{}
	defaultLog := slog.Default()
	logged := &errorsLogged{}
	slog.SetDefault(slog.New(logged))
	t.Cleanup(func() { slog.SetDefault(defaultLog) })

	exp := time.Unix(1_900_000_000, 0)
	token := jwtExpiring(exp)
	url, js := verifyingStream(t, token)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const first, second = 60, 20
	publish(t, js, first)

	timers := make(chan func(), 64)
	with := opts(url, 5)
	with.AckWait = time.Minute
	with.TokenFile = tokenFile(t, token)
	with.RefreshLead = 2 * time.Second
	with.Log = out.logger()
	with.leaseHook = func(l *tokenLease) {
		l.now = func() time.Time { return exp.Add(-3 * time.Second) }
		l.after = func(_ time.Duration, f func()) func() bool {
			timers <- f
			return func() bool { return true }
		}
	}

	var fired atomic.Int32
	reconnects := func() int { return strings.Count(out.String(), "msg=\"reconnected to the stream\"") }
	// fire runs the lease's scheduled reconnect, if it has one, and waits for
	// the connection to be back.
	fire := func() {
		t.Helper()
		waitFor(t, "the previous planned reconnect to complete", func() bool { return reconnects() >= int(fired.Load()) })
		before := reconnects()
		select {
		case f := <-timers:
			f()
		default:
			t.Error("the lease had no reconnect scheduled")
			return
		}
		waitFor(t, "the planned reconnect to complete", func() bool { return reconnects() > before })
	}

	var drained atomic.Bool
	into := &tally{}
	into.onCall = func(call int) {
		// Mid-run, between taking a batch and acknowledging it. The last
		// reconnect is let finish first: asked again while one is under way,
		// the client folds the two into one.
		if call > 3 || drained.Load() {
			return
		}
		waitFor(t, "the previous planned reconnect to complete", func() bool { return reconnects() >= int(fired.Load()) })
		select {
		case f := <-timers:
			fired.Add(1)
			f()
		default:
			t.Error("the lease had no reconnect scheduled")
		}
	}
	stop, err := consume(ctx, with, into)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	waitFor(t, "the first records to reach the writer", func() bool { return into.done() == first || t.Failed() })

	drained.Store(true)
	// Drained: the consumer is parked in a fetch. Cut it off, then publish
	// more on the stream and see them arrive.
	fire()
	publish(t, js, second)
	waitFor(t, "the records published after the reconnect", func() bool { return into.done() == first+second || t.Failed() })
	if t.Failed() {
		t.FailNow()
	}
	stop()

	if n, want := reconnects(), int(fired.Load())+1; n != want {
		t.Fatalf("%d planned reconnects happened, want %d:\n%s", n, want, out.String())
	}
	if fired.Load() == 0 {
		t.Fatal("no planned reconnect fired while the consumer was working")
	}
	if dup := into.repeated(); len(dup) != 0 {
		t.Fatalf("%d records were handed to the writer more than once: %v", len(dup), dup)
	}
	if msgs := logged.consumer(); len(msgs) != 0 {
		t.Fatalf("the consumer logged at ERROR across a planned reconnect: %v", msgs)
	}
	if strings.Contains(out.String(), "level=ERROR") {
		t.Fatalf("a planned reconnect logged an error:\n%s", out.String())
	}

	// Nothing is left unacknowledged on the stream: every ack reached it.
	s, err := js.Stream(context.Background(), "AUDIT")
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Consumer(context.Background(), with.Durable)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "every acknowledgement to reach the stream", func() bool {
		info, err := c.Info(context.Background())
		return err == nil && info.NumAckPending == 0 && info.NumPending == 0
	})
}
