// Package observetest is what the cursor indexer is held to, whatever it is
// run over: the same cases against the in-memory archive, a real S3 and a real
// Postgres, because the claims are about the listing and the transaction and
// both are the places a double is kinder than the real thing.
package observetest

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/truvity/audit/index"
	"github.com/truvity/audit/internal/observe"
	"github.com/truvity/audit/internal/recobj"
	"github.com/truvity/audit/internal/ulid"
	"github.com/truvity/audit/sdk/record"
	"github.com/truvity/audit/store"
)

// Env is what a suite runs over.
type Env struct {
	// Store is an empty archive that objects can be put into.
	Store store.Store
	// Cursors opens the cursors and the index behind them. Called again it
	// opens the same ones, as a restarted process would.
	Cursors func() observe.Cursors
	// Indexed is the ids the index holds for a profile, sorted.
	Indexed func(profile string) []string
}

// Base is the moment the suite's clock starts at.
var Base = time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)

// Fields is what the suite's records are indexed by.
func Fields(context.Context, *record.Record) (index.Fields, error) {
	return index.Fields{Filter: []string{"/credential_type"}, Facet: []string{"/credential_type"}}, nil
}

// Record is a record with a stable id.
func Record(t testing.TB, id, tenant string, recorded time.Time) *record.Record {
	t.Helper()
	data, err := structpb.NewStruct(map[string]any{"credential_type": "pid"})
	if err != nil {
		t.Fatal(err)
	}
	r := &record.Record{
		Id: id, SchemaVersion: record.SchemaVersion, CatalogueVersion: "1.0.0", Source: "wallet",
		Action: "wallet.credential.issued", TenantId: tenant, Data: data,
	}
	record.Assign(r)
	r.RecordedAt = timestamppb.New(recorded)
	r.OccurredAt = timestamppb.New(recorded.Add(-time.Minute))
	return r
}

// ID is the nth id of a series, a valid UUID.
func ID(n int) string { return fmt.Sprintf("018f0000-0000-7000-8000-%012d", n) }

// Put writes an object of the shape the writer writes, taken at the moment
// given: the key's ULID is that moment. Two objects taken at one moment are
// told apart by their first record's id.
func Put(t testing.TB, s store.Store, profile, tenant string, taken time.Time, records ...*record.Record) string {
	t.Helper()
	var lines [][]byte
	for _, r := range records {
		canonical, err := record.Canonical(r)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, recobj.EncodeLine(canonical))
	}
	body, meta := recobj.Encode(lines)
	h := fnv.New32a()
	_, _ = h.Write([]byte(records[0].GetId()))
	key := store.RecordKey(profile, tenant, taken, ulid.From(taken, uint64(h.Sum32())))
	if err := s.Put(context.Background(), store.Object{
		Key: key, Body: body, Metadata: meta, ContentType: recobj.ContentType, Encoding: recobj.Encoding,
		RetainUntil: time.Now().UTC().Add(48 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	return key
}

// Clock is a clock a test moves.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock starts at Base.
func NewClock() *Clock { return &Clock{now: Base} }

// Now is the time.
func (c *Clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

// Set moves it.
func (c *Clock) Set(t time.Time) { c.mu.Lock(); defer c.mu.Unlock(); c.now = t }

// Run holds an indexer to its claims over one environment. Each case gets a
// fresh one from mk.
func Run(t *testing.T, mk func(t *testing.T) Env) {
	t.Helper()
	const settle = 2 * time.Minute
	ctx := context.Background()

	indexer := func(env Env, clock *Clock, cursors observe.Cursors) *observe.Indexer {
		return &observe.Indexer{
			Store: env.Store, Cursors: cursors, Fields: Fields, Settle: settle, Now: clock.Now,
		}
	}
	ids := func(ns ...int) []string {
		out := make([]string, len(ns))
		for i, n := range ns {
			out[i] = ID(n)
		}
		sort.Strings(out)
		return out
	}
	same := func(t *testing.T, got, want []string) {
		t.Helper()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("the index holds %v, want %v", got, want)
		}
	}

	t.Run("indexes every tenant and profile it finds by listing", func(t *testing.T) {
		env, clock := mk(t), NewClock()
		Put(t, env.Store, "security", "acme", Base, Record(t, ID(1), "acme", Base))
		Put(t, env.Store, "security", "globex", Base.Add(time.Second), Record(t, ID(2), "globex", Base))
		Put(t, env.Store, "history", "acme", Base.Add(2*time.Second), Record(t, ID(3), "acme", Base))
		clock.Set(Base.Add(time.Hour))

		report, err := indexer(env, clock, env.Cursors()).Pass(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if report.Tenants != 3 || report.Objects != 3 || report.Rows != 3 {
			t.Fatalf("report %+v, want 3 tenants, 3 objects and 3 rows", report)
		}
		same(t, env.Indexed("security"), ids(1, 2))
		same(t, env.Indexed("history"), ids(3))
	})

	t.Run("keeps its cursor behind the settle window", func(t *testing.T) {
		env, clock := mk(t), NewClock()
		old := Put(t, env.Store, "security", "acme", Base, Record(t, ID(1), "acme", Base))
		fresh := Put(t, env.Store, "security", "acme", Base.Add(90*time.Second), Record(t, ID(2), "acme", Base))
		x := indexer(env, clock, env.Cursors())

		clock.Set(Base.Add(settle + time.Second)) // the first is old enough, the second is not
		report, err := x.Pass(ctx)
		if err != nil {
			t.Fatal(err)
		}
		same(t, env.Indexed("security"), ids(1))
		if want := Base.Add(90*time.Second + settle); !report.Next.Equal(want) {
			t.Errorf("the pass says to come back at %s, want %s", report.Next, want)
		}
		if got, _ := env.Cursors().Cursor(ctx, "security", "acme"); got != old {
			t.Errorf("the cursor is %q, want it at the object that was old enough, %q", got, old)
		}

		clock.Set(Base.Add(90*time.Second + settle + time.Second))
		if _, err := x.Pass(ctx); err != nil {
			t.Fatal(err)
		}
		same(t, env.Indexed("security"), ids(1, 2))
		if got, _ := env.Cursors().Cursor(ctx, "security", "acme"); got != fresh {
			t.Errorf("the cursor is %q, want %q", got, fresh)
		}
	})

	// The reason for the window. A put's key is fixed when it starts and the
	// object is visible when it ends, so a key that sorts earlier can appear
	// after one that sorts later. A cursor that had moved past the later key
	// would never see it.
	t.Run("indexes a put that lands behind a key already listed", func(t *testing.T) {
		env, clock := mk(t), NewClock()
		x := indexer(env, clock, env.Cursors())

		Put(t, env.Store, "security", "acme", Base.Add(5*time.Second), Record(t, ID(2), "acme", Base))
		clock.Set(Base.Add(30 * time.Second)) // both are inside the window
		if _, err := x.Pass(ctx); err != nil {
			t.Fatal(err)
		}
		same(t, env.Indexed("security"), nil)

		// The slow put, whose key was taken before the one above, lands now.
		Put(t, env.Store, "security", "acme", Base, Record(t, ID(1), "acme", Base))
		clock.Set(Base.Add(time.Minute)) // still inside the window
		if _, err := x.Pass(ctx); err != nil {
			t.Fatal(err)
		}
		same(t, env.Indexed("security"), nil)

		clock.Set(Base.Add(10*time.Second + settle))
		if _, err := x.Pass(ctx); err != nil {
			t.Fatal(err)
		}
		same(t, env.Indexed("security"), ids(1, 2))
	})

	t.Run("resumes from its cursor after a restart", func(t *testing.T) {
		env, clock := mk(t), NewClock()
		first := Put(t, env.Store, "security", "acme", Base, Record(t, ID(1), "acme", Base))
		clock.Set(Base.Add(time.Hour))
		if _, err := indexer(env, clock, env.Cursors()).Pass(ctx); err != nil {
			t.Fatal(err)
		}

		second := Put(t, env.Store, "security", "acme", Base.Add(2*time.Hour), Record(t, ID(2), "acme", Base))
		clock.Set(Base.Add(3 * time.Hour))
		var seen []string
		restarted := indexer(env, clock, env.Cursors()) // a new process, the same database
		restarted.OnObject = func(_, key string, _ int, _ time.Duration) { seen = append(seen, key) }
		if _, err := restarted.Pass(ctx); err != nil {
			t.Fatal(err)
		}
		if len(seen) != 1 || seen[0] != second {
			t.Fatalf("a restarted indexer read %v, want only %s (the cursor was past %s)", seen, second, first)
		}
		same(t, env.Indexed("security"), ids(1, 2))
	})

	t.Run("indexes nothing twice", func(t *testing.T) {
		env, clock := mk(t), NewClock()
		for i := 1; i <= 3; i++ {
			Put(t, env.Store, "security", "acme", Base.Add(time.Duration(i)*time.Second), Record(t, ID(i), "acme", Base))
		}
		clock.Set(Base.Add(time.Hour))
		cursors := env.Cursors()
		x := indexer(env, clock, cursors)
		if _, err := x.Pass(ctx); err != nil {
			t.Fatal(err)
		}
		again, err := x.Pass(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if again.Objects != 0 {
			t.Errorf("a second pass read %d objects", again.Objects)
		}

		// A rewound cursor makes the indexer read everything again, which
		// changes nothing: the index counts a record once.
		if err := cursors.ResetCursor(ctx, "security", ""); err != nil {
			t.Fatal(err)
		}
		rerun, err := x.Pass(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if rerun.Objects != 3 {
			t.Errorf("after a reset the pass read %d objects, want all 3", rerun.Objects)
		}
		same(t, env.Indexed("security"), ids(1, 2, 3))
	})

	// A wake-up that never comes costs latency and nothing else: the poll finds
	// the object.
	t.Run("finds an object by polling when the wake-up was lost", func(t *testing.T) {
		env := mk(t)
		x := &observe.Indexer{
			Store: env.Store, Cursors: env.Cursors(), Fields: Fields,
			Settle: time.Millisecond, Interval: 20 * time.Millisecond,
			Wake: make(chan struct{}), // nobody ever signals it
		}
		running, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); _ = x.Run(running) }()
		defer func() { stop(); <-done }()

		taken := time.Now().UTC().Add(-time.Hour)
		Put(t, env.Store, "security", "acme", taken, Record(t, ID(1), "acme", taken))
		deadline := time.Now().Add(20 * time.Second)
		for len(env.Indexed("security")) == 0 {
			if time.Now().After(deadline) {
				t.Fatal("the object was never indexed: a lost wake-up must cost latency and not data")
			}
			time.Sleep(20 * time.Millisecond)
		}
	})

	t.Run("a wake-up makes a pass run now", func(t *testing.T) {
		env := mk(t)
		wake := make(chan struct{}, 1)
		x := &observe.Indexer{
			Store: env.Store, Cursors: env.Cursors(), Fields: Fields,
			Settle: time.Millisecond, Interval: time.Hour, Wake: wake,
		}
		running, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); _ = x.Run(running) }()
		defer func() { stop(); <-done }()

		time.Sleep(200 * time.Millisecond) // the first pass, over nothing
		taken := time.Now().UTC().Add(-time.Hour)
		Put(t, env.Store, "security", "acme", taken, Record(t, ID(1), "acme", taken))
		observe.Signal(wake)
		deadline := time.Now().Add(20 * time.Second)
		for len(env.Indexed("security")) == 0 {
			if time.Now().After(deadline) {
				t.Fatal("a wake-up did not shorten an interval of an hour")
			}
			time.Sleep(20 * time.Millisecond)
		}
	})

	t.Run("skips an object that will never read and goes on", func(t *testing.T) {
		env, clock := mk(t), NewClock()
		Put(t, env.Store, "security", "acme", Base, Record(t, ID(1), "acme", Base))
		bad := Put(t, env.Store, "security", "acme", Base.Add(time.Second), Record(t, ID(2), "acme", Base))
		Put(t, env.Store, "security", "acme", Base.Add(2*time.Second), Record(t, ID(3), "acme", Base))
		if err := corrupt(env.Store, bad); err != nil {
			t.Skip(err)
		}
		clock.Set(Base.Add(time.Hour))
		var skipped []string
		x := indexer(env, clock, env.Cursors())
		x.OnDeferred = func(_, key string, permanent bool, _ error) {
			if permanent {
				skipped = append(skipped, key)
			}
		}
		if _, err := x.Pass(ctx); err != nil {
			t.Fatal(err)
		}
		same(t, env.Indexed("security"), ids(1, 3))
		if len(skipped) != 1 || skipped[0] != bad {
			t.Fatalf("skipped %v, want %s named", skipped, bad)
		}
	})

	t.Run("stops at an object it cannot fetch and resumes there", func(t *testing.T) {
		env, clock := mk(t), NewClock()
		Put(t, env.Store, "security", "acme", Base, Record(t, ID(1), "acme", Base))
		broken := Put(t, env.Store, "security", "acme", Base.Add(time.Second), Record(t, ID(2), "acme", Base))
		Put(t, env.Store, "security", "acme", Base.Add(2*time.Second), Record(t, ID(3), "acme", Base))
		clock.Set(Base.Add(time.Hour))

		cursors := env.Cursors()
		flaky := &failing{Store: env.Store, key: broken}
		x := indexer(env, clock, cursors)
		x.Store = flaky
		var retries int
		x.OnDeferred = func(_, _ string, permanent bool, _ error) {
			if !permanent {
				retries++
			}
		}
		if _, err := x.Pass(ctx); err == nil {
			t.Fatal("a pass over an object that could not be fetched reported success")
		}
		if retries != 1 {
			t.Errorf("%d retries reported, want 1", retries)
		}
		same(t, env.Indexed("security"), ids(1)) // the cursor is at the last good object, not past it

		flaky.fixed()
		if _, err := x.Pass(ctx); err != nil {
			t.Fatal(err)
		}
		same(t, env.Indexed("security"), ids(1, 2, 3))
	})

	t.Run("moves the cursor with the rows and no further", func(t *testing.T) {
		env, clock := mk(t), NewClock()
		var keys []string
		for i := 1; i <= 4; i++ {
			keys = append(keys, Put(t, env.Store, "security", "acme", Base.Add(time.Duration(i)*time.Second),
				Record(t, ID(i), "acme", Base)))
		}
		clock.Set(Base.Add(time.Hour))
		x := indexer(env, clock, env.Cursors())
		x.Batch = 1 // a transaction per object
		var committed []string
		x.OnObject = func(_, key string, _ int, _ time.Duration) { committed = append(committed, key) }
		if _, err := x.Pass(ctx); err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(committed) != fmt.Sprint(keys) {
			t.Fatalf("committed %v, want every object in key order %v", committed, keys)
		}
		if got, _ := env.Cursors().Cursor(ctx, "security", "acme"); got != keys[3] {
			t.Fatalf("the cursor is %q, want %q", got, keys[3])
		}
	})
}

// corrupt overwrites an object with bytes that are not zstd, if the store lets
// a test: a locked bucket does not, which is the point of one.
func corrupt(s store.Store, key string) error {
	type replacer interface{ Replace(key string, body []byte) }
	if r, ok := s.(replacer); ok {
		r.Replace(key, []byte("this is not zstd"))
		return nil
	}
	return errors.New("this store cannot be made to hold a corrupt object")
}

// failing is a store one object of which cannot be fetched until it is fixed.
type failing struct {
	store.Store
	key string
	mu  sync.Mutex
	ok  bool
}

func (f *failing) Get(ctx context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	broken := key == f.key && !f.ok
	f.mu.Unlock()
	if broken {
		return nil, errors.New("the store is unreachable")
	}
	return f.Store.Get(ctx, key)
}

func (f *failing) fixed() { f.mu.Lock(); f.ok = true; f.mu.Unlock() }
