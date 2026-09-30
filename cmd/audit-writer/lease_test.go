package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

// jwtExpiring is a token shaped like a projected ServiceAccount token: three
// dot-separated parts whose middle one carries an exp claim. The signature is
// not a signature, because nothing here verifies it.
func jwtExpiring(exp time.Time) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." +
		enc([]byte(fmt.Sprintf(`{"aud":["nats"],"exp":%d,"sub":"system:serviceaccount:ns:sa"}`, exp.Unix()))) +
		".c2lnbmF0dXJl"
}

func TestTheExpiryIsReadFromTheTokenWithoutVerifyingIt(t *testing.T) {
	at := time.Unix(1_900_000_000, 0)
	for _, tc := range []struct {
		name, token string
		want        time.Time
	}{
		{"a JWT", jwtExpiring(at), at},
		{"padded", strings.Replace(jwtExpiring(at), ".", "==.", 2), at},
		{"no exp claim", "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"x"}`)) + ".sig", time.Time{}},
		{"not a JWT", "s3cret", time.Time{}},
		{"not base64", "a.!!!.c", time.Time{}},
		{"not JSON", "a." + base64.RawURLEncoding.EncodeToString([]byte("nope")) + ".c", time.Time{}},
		{"exp not a number", "a." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":"soon"}`)) + ".c", time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := expiryOf(tc.token); !got.Equal(tc.want) {
				t.Fatalf("expiryOf = %v, want %v", got, tc.want)
			}
		})
	}
}

// clock is a lease's time, stopped, and its timers, which fire when told.
type clock struct {
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	in      time.Duration
	fire    func()
	stopped bool
}

func (c *clock) lease(lead time.Duration) *tokenLease {
	l := newTokenLease("unused", lead)
	l.now = func() time.Time { return c.now }
	l.after = func(d time.Duration, f func()) func() bool {
		ft := &fakeTimer{in: d, fire: f}
		c.timers = append(c.timers, ft)
		return func() bool { ft.stopped = true; return true }
	}
	return l
}

func (c *clock) live() []*fakeTimer {
	var out []*fakeTimer
	for _, ft := range c.timers {
		if !ft.stopped {
			out = append(out, ft)
		}
	}
	return out
}

type reconnects struct{ n atomic.Int32 }

func (r *reconnects) ForceReconnect() error { r.n.Add(1); return nil }

// The connection is reopened the lead before the presented token expires: the
// kubelet has long renewed the file by then, and the broker has not yet ended
// the session, so the change of token is the client's and not a fault.
func TestTheConnectionIsReopenedBeforeTheTokenExpires(t *testing.T) {
	c := &clock{now: time.Unix(1_900_000_000, 0)}
	l := c.lease(30 * time.Second)
	conn := &reconnects{}
	l.conn = conn

	l.presented(c.now.Add(time.Hour))
	timers := c.live()
	if len(timers) != 1 {
		t.Fatalf("%d reconnects scheduled, want 1", len(timers))
	}
	if want := time.Hour - 30*time.Second; timers[0].in != want {
		t.Fatalf("the reconnect is due in %s, want %s", timers[0].in, want)
	}
	timers[0].fire()
	if conn.n.Load() != 1 {
		t.Fatalf("the connection was reopened %d times, want once", conn.n.Load())
	}
}

// Every connect presents a token and so ends the last schedule: a reconnect
// for any reason reads the file again, and only the token now presented
// decides when the next one is due.
func TestEachTokenPresentedReplacesTheSchedule(t *testing.T) {
	c := &clock{now: time.Unix(1_900_000_000, 0)}
	l := c.lease(30 * time.Second)
	l.conn = &reconnects{}

	l.presented(c.now.Add(time.Hour))
	c.now = c.now.Add(10 * time.Minute)
	l.presented(c.now.Add(time.Hour))

	timers := c.live()
	if len(timers) != 1 {
		t.Fatalf("%d reconnects scheduled, want only the latest", len(timers))
	}
	if want := time.Hour - 30*time.Second; timers[0].in != want {
		t.Fatalf("the reconnect is due in %s, want %s", timers[0].in, want)
	}
}

// A token that expires within the lead is the one the file still holds, so
// reconnecting early would present it again, and again: no schedule. The
// broker's expiry, and the reconnect after it, read the file anew.
func TestATokenAboutToExpireIsNotReconnectedInALoop(t *testing.T) {
	c := &clock{now: time.Unix(1_900_000_000, 0)}
	l := c.lease(30 * time.Second)
	l.conn = &reconnects{}

	for _, in := range []time.Duration{30 * time.Second, time.Second, -time.Minute} {
		l.presented(c.now.Add(in))
		if n := len(c.live()); n != 0 {
			t.Fatalf("a token expiring in %s scheduled %d reconnects, want none", in, n)
		}
	}
}

// A token with no expiry — a static one — schedules nothing, and a closed
// connection is never reopened.
func TestNothingIsScheduledWithoutAnExpiryOrAfterClose(t *testing.T) {
	c := &clock{now: time.Unix(1_900_000_000, 0)}
	l := c.lease(30 * time.Second)
	conn := &reconnects{}
	l.conn = conn

	l.presented(time.Time{})
	if n := len(c.live()); n != 0 {
		t.Fatalf("a token with no expiry scheduled %d reconnects", n)
	}

	l.presented(c.now.Add(time.Hour))
	fire := c.live()[0].fire
	l.stop()
	if n := len(c.live()); n != 0 {
		t.Fatalf("closing left %d reconnects scheduled", n)
	}
	fire() // a timer that had already fired as it was stopped
	l.presented(c.now.Add(time.Hour))
	if conn.n.Load() != 0 || len(c.live()) != 0 {
		t.Fatal("a closed connection was reopened")
	}
}

// logs captures what the process logs, by level.
type logs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// logger is a logger of this test's own, so that what other tests' connections
// log as they wind down is not mistaken for this one's.
func (l *logs) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(l, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// The whole of the change of token, end to end: a receiver whose token is
// about to expire reopens its connection on its own schedule, while it is
// publishing, and not one record fails or is written twice. The log says so at
// INFO, and says nothing at ERROR.
//
// The lease's clock and timer are the test's, so that the reconnect happens
// when the test says, in the middle of a publish, and not when a loaded
// machine's scheduler gets to it.
func TestAPlannedReconnectLosesAndFailsNothing(t *testing.T) {
	out := &logs{}

	exp := time.Unix(1_900_000_000, 0)
	token := jwtExpiring(exp)
	url, js := verifyingStream(t, token)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Every timer the lease sets is handed over, so the test can fire it.
	timers := make(chan func(), 16)
	with := opts(url, 10)
	with.TokenFile = tokenFile(t, token)
	with.RefreshLead = 2 * time.Second
	with.Log = out.logger()
	with.leaseHook = func(l *tokenLease) {
		l.now = func() time.Time { return exp.Add(-3 * time.Second) }
		l.after = func(d time.Duration, f func()) func() bool {
			if d != time.Second {
				t.Errorf("the reconnect is due in %s, want 1s", d)
			}
			timers <- f
			return func() bool { return true }
		}
	}
	p, stop, err := publisherFor(ctx, with)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	var fire func()
	select {
	case fire = <-timers:
	default:
		t.Fatal("connecting scheduled no reconnect")
	}

	var sent atomic.Int64
	publish := func() {
		batch := make([]*record.Record, 0, 20)
		for range 20 {
			r := &record.Record{
				CatalogueVersion: "1.0.0", Source: "wallet", TenantId: "acme",
				Action: "wallet.credential.issued", Operation: auditv1.Operation_OPERATION_CREATE,
			}
			record.Assign(r)
			batch = append(batch, r)
		}
		res, err := p.Write(ctx, &sink.Request{Records: batch, Delivery: sink.Block})
		if err != nil {
			t.Errorf("a publish failed across the planned reconnect after %d records: %v", sent.Load(), err)
			return
		}
		if res.Accepted != len(batch) {
			t.Errorf("accepted %d of %d", res.Accepted, len(batch))
			return
		}
		sent.Add(int64(len(batch)))
	}

	// Publish continuously, and reconnect in the middle of it.
	done := make(chan struct{})
	halt := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-halt:
				return
			default:
			}
			publish()
			if t.Failed() {
				return
			}
		}
	}()
	waitFor(t, "some records to be published", func() bool { return sent.Load() >= 100 || t.Failed() })
	fire()
	waitFor(t, "the planned reconnect to complete", func() bool {
		return strings.Contains(out.String(), "msg=\"reconnected to the stream\"") || t.Failed()
	})
	// Records published after the reconnect, on the new connection.
	after := sent.Load()
	waitFor(t, "records to be published on the new connection", func() bool { return sent.Load() >= after+100 || t.Failed() })
	close(halt)
	<-done
	if t.Failed() {
		t.FailNow()
	}

	logged := out.String()
	if !strings.Contains(logged, "level=INFO msg=\"reconnecting to the stream before its token expires\"") {
		t.Fatalf("no planned reconnect was logged at INFO:\n%s", logged)
	}
	if strings.Contains(logged, "level=ERROR") {
		t.Fatalf("a planned reconnect logged an error:\n%s", logged)
	}

	s, err := js.Stream(ctx, "AUDIT")
	if err != nil {
		t.Fatal(err)
	}
	info, err := s.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.State.Msgs != uint64(sent.Load()) {
		t.Fatalf("the stream holds %d messages for %d records published", info.State.Msgs, sent.Load())
	}
}

// waitFor polls until cond holds. The bound is only how long a broken build
// takes to be told so; a working one is not waiting on it.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
