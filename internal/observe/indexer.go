package observe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/truvity/audit/index"
	"github.com/truvity/audit/internal/ulid"
	"github.com/truvity/audit/store"
)

// Cursors is where an indexer keeps how far it has read, and where the rows
// go. The two are one interface because they are one transaction: the cursor
// moves only with the rows it covers, so a crash leaves both or neither and a
// restarted indexer resumes without a row missing behind its cursor.
type Cursors interface {
	// Cursor is the key of the last object indexed under a profile and
	// tenant, or "" when none has been.
	Cursor(ctx context.Context, profile, tenant string) (string, error)
	// Advance indexes the rows and moves the cursor to lastKey, atomically.
	// Rows already indexed change nothing; the cursor never moves backwards.
	Advance(ctx context.Context, profile, tenant, lastKey string, rows []index.Row) error
	// ResetCursor forgets the cursor of a tenant, or of every tenant of the
	// profile when tenant is empty.
	ResetCursor(ctx context.Context, profile, tenant string) error
}

// Defaults of an Indexer.
const (
	// DefaultSettle is how far behind now the cursor stays. It has to be
	// longer than a writer's put can take, so that no put is in flight at that
	// age, and longer than the clocks of the writers and of this process can
	// disagree.
	DefaultSettle = 2 * time.Minute
	// DefaultInterval is the poll: how often a pass runs when nothing woke it.
	DefaultInterval = 30 * time.Second
	// DefaultBatch is how many rows are indexed in one transaction.
	DefaultBatch = 500
)

// Indexer follows the archive's listing and indexes what it finds.
//
// A pass discovers the profiles and tenants by listing, and for each lists the
// keys after the cursor, up to now minus the settle window. Keys sort by ingest
// time, so that listing is what has arrived since. An object's key is fixed
// when its put starts and the object is visible when it ends, so a later key
// can be visible before an earlier one: the settle window is what keeps the
// cursor from passing a put still in flight.
type Indexer struct {
	Store   store.Store
	Cursors Cursors
	Fields  Fields

	// Settle is the settle window. Default 2m; see DefaultSettle.
	Settle time.Duration
	// Interval is the poll. Default 30s.
	Interval time.Duration
	// Batch is how many rows are written in one transaction. Default 500. A
	// pass always ends a transaction at an object's end, so one object is
	// never split across two.
	Batch int
	// Profiles, when set, limits what is followed. Unset follows every profile
	// the archive has.
	Profiles []string
	// Wake, when set, makes a pass run now. A signal carries nothing the pass
	// depends on, so a lost one costs latency and nothing else.
	Wake <-chan struct{}

	// OnObject is called for each object whose rows are in the index, with how
	// long after the object reached the archive that was.
	OnObject func(profile, key string, rows int, lag time.Duration)
	// OnDeferred is called for an object (or line) the indexer could not
	// index. permanent says whether a retry would help: an object that does not
	// decode never will and is skipped, an unreachable store or a missing
	// catalogue is retried by the next pass.
	OnDeferred func(profile, key string, permanent bool, err error)

	// Now is the clock, for tests.
	Now func() time.Time
	// Log, default slog.Default().
	Log *slog.Logger
}

// Report is what a pass did.
type Report struct {
	Tenants int
	Objects int
	Rows    int
	// Next is the earliest moment a key the pass left alone for being too
	// new will be old enough, or the zero time when it left none.
	Next time.Time
}

func (x *Indexer) now() time.Time {
	if x.Now != nil {
		return x.Now().UTC()
	}
	return time.Now().UTC()
}

func (x *Indexer) settle() time.Duration {
	if x.Settle > 0 {
		return x.Settle
	}
	return DefaultSettle
}

func (x *Indexer) interval() time.Duration {
	if x.Interval > 0 {
		return x.Interval
	}
	return DefaultInterval
}

func (x *Indexer) batch() int {
	if x.Batch > 0 {
		return x.Batch
	}
	return DefaultBatch
}

func (x *Indexer) log() *slog.Logger {
	if x.Log != nil {
		return x.Log
	}
	return slog.Default()
}

// Run passes until the context ends: at once, then whenever woken, when the
// oldest key a pass had to leave is old enough, or at the interval, whichever
// is first. A pass that fails is logged and tried again at the next of those:
// the indexer outlives a database or a bucket that is away for a while, which
// is the point of it not being on the write path.
func (x *Indexer) Run(ctx context.Context) error {
	if x.Store == nil || x.Cursors == nil || x.Fields == nil {
		return errors.New("observe: an indexer needs a store, cursors and a way to find fields")
	}
	for {
		report, err := x.Pass(ctx)
		if err != nil && ctx.Err() == nil {
			x.log().Error("an indexing pass failed; the next one resumes from the cursors", "error", err)
		}
		if report.Objects > 0 {
			x.log().Info("indexed", "tenants", report.Tenants, "objects", report.Objects, "rows", report.Rows)
		}
		wait := x.interval()
		if !report.Next.IsZero() {
			// The key that was too new is old enough at Next; there is no
			// point in looking sooner, and none in waiting past it.
			if until := report.Next.Sub(x.now()); until < wait {
				wait = max(until, 0) + 100*time.Millisecond
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-x.Wake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// Pass indexes everything that is old enough, once. It discovers the profiles
// and tenants by listing, and continues past a tenant that fails, so that one
// tenant's trouble does not hold back the others; every failure is in the
// error it returns.
func (x *Indexer) Pass(ctx context.Context) (Report, error) {
	horizon := x.now().Add(-x.settle())
	var report Report
	profiles := x.Profiles
	if len(profiles) == 0 {
		var err error
		if profiles, err = store.Profiles(ctx, x.Store); err != nil {
			return report, fmt.Errorf("observe: listing the profiles: %w", err)
		}
	}
	var errs []error
	for _, profile := range profiles {
		tenants, err := store.Tenants(ctx, x.Store, profile)
		if err != nil {
			errs = append(errs, fmt.Errorf("observe: listing the tenants of %s: %w", profile, err))
			continue
		}
		for _, tenant := range tenants {
			if ctx.Err() != nil {
				return report, errors.Join(append(errs, ctx.Err())...)
			}
			got, err := x.tenant(ctx, profile, tenant, horizon)
			report.Tenants++
			report.Objects += got.Objects
			report.Rows += got.Rows
			if !got.Next.IsZero() && (report.Next.IsZero() || got.Next.Before(report.Next)) {
				report.Next = got.Next
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("observe: %s/%s: %w", profile, tenant, err))
			}
		}
	}
	return report, errors.Join(errs...)
}

// errUnripe stops a walk at the first key too new to index.
var errUnripe = errors.New("observe: a key newer than the settle window")

// since is a time before any archive: the walk starts after the cursor, or at
// the beginning of the tenant's keys.
var since = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

type done struct {
	key  string
	rows int
	at   time.Time
}

func (x *Indexer) tenant(ctx context.Context, profile, tenant string, horizon time.Time) (Report, error) {
	var report Report
	after, err := x.Cursors.Cursor(ctx, profile, tenant)
	if err != nil {
		return report, err
	}

	var (
		rows    []index.Row
		pending []done
		last    string
	)
	flush := func() error {
		if last == "" {
			return nil
		}
		if err := x.Cursors.Advance(ctx, profile, tenant, last, rows); err != nil {
			return err
		}
		// Only now are the rows in the index: the lag is to here.
		at := x.now()
		for _, d := range pending {
			report.Objects++
			report.Rows += d.rows
			if x.OnObject != nil {
				x.OnObject(profile, d.key, d.rows, at.Sub(d.at))
			}
		}
		rows, pending, last = rows[:0], pending[:0], ""
		return nil
	}

	walked := store.WalkHours(ctx, x.Store, profile, tenant, since, horizon, after, func(e store.Entry) error {
		parsed, ok := store.ParseRecordKey(e.Key)
		if !ok {
			// Not a record object, and never going to be one. Passing it is
			// harmless and listing it again for ever is not.
			last = e.Key
			return nil
		}
		ingested, err := ulid.Time(parsed.ULID)
		if err != nil {
			last = e.Key
			return nil
		}
		if ingested.After(horizon) {
			report.Next = ingested.Add(x.settle())
			return errUnripe
		}
		got, unreadable, err := ReadObject(ctx, x.Store, e.Key, x.Fields)
		for _, what := range unreadable {
			x.log().Error("an object the archive holds could not be read, and is skipped: it will not read later either",
				"profile", profile, "object", what)
			if x.OnDeferred != nil {
				x.OnDeferred(profile, what, true, errors.New("unreadable"))
			}
		}
		if err != nil {
			if x.OnDeferred != nil {
				x.OnDeferred(profile, e.Key, false, err)
			}
			return err
		}
		rows = append(rows, got...)
		last = e.Key
		// The put's own moment is what the lag is from; a store that does not
		// say falls back to the key's.
		put := e.Modified
		if put.IsZero() {
			put = ingested
		}
		pending = append(pending, done{key: e.Key, rows: len(got), at: put})
		if len(rows) >= x.batch() {
			return flush()
		}
		return nil
	})
	if errors.Is(walked, errUnripe) {
		walked = nil
	}
	// Whatever was read whole before a failure is indexed: the cursor is
	// where the last good object was, so the next pass resumes at the one that
	// failed and not at the beginning.
	if ferr := flush(); ferr != nil && walked == nil {
		walked = ferr
	}
	return report, walked
}

// Memory is cursors over any index.Indexer, kept in memory: for tests, and for
// an index that has no transaction to share. It is not atomic: a crash between
// the rows and the cursor leaves rows indexed and the cursor behind, which the
// idempotence of indexing makes harmless, and a store with a transaction
// (index/postgres) does better.
type Memory struct {
	Index index.Indexer

	mu      sync.Mutex
	cursors map[[2]string]string
}

// Cursor implements Cursors.
func (m *Memory) Cursor(_ context.Context, profile, tenant string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cursors[[2]string{profile, tenant}], nil
}

// Advance implements Cursors.
func (m *Memory) Advance(ctx context.Context, profile, tenant, lastKey string, rows []index.Row) error {
	if len(rows) > 0 {
		if err := m.Index.Index(ctx, profile, rows); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cursors == nil {
		m.cursors = map[[2]string]string{}
	}
	if k := [2]string{profile, tenant}; lastKey > m.cursors[k] {
		m.cursors[k] = lastKey
	}
	return nil
}

// ResetCursor implements Cursors.
func (m *Memory) ResetCursor(_ context.Context, profile, tenant string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.cursors {
		if k[0] == profile && (tenant == "" || k[1] == tenant) {
			delete(m.cursors, k)
		}
	}
	return nil
}

// Positions is every cursor held, for a test to read.
func (m *Memory) Positions() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	keys := make([][2]string, 0, len(m.cursors))
	for k := range m.cursors {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i][0]+keys[i][1] < keys[j][0]+keys[j][1] })
	for _, k := range keys {
		out[k[0]+"/"+k[1]] = m.cursors[k]
	}
	return out
}
