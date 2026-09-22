// Package writer is the audit writer as a library: the thing that takes records,
// splits them into the copies their profiles keep, pseudonymises, and puts them
// into the locked archive.
//
// An application embeds it when it has no stream and no central writer to hand
// records to: its own emitter writes straight into the writer in process, and
// the writer writes straight to the bucket. It is the same writer the
// audit-writer binary runs — that binary is built on this package — so an
// embedded one keeps the same promises: nothing is acknowledged before it is in
// the archive, a record the writer cannot take is dead-lettered and never
// dropped, and the writer keeps an account of itself in the archive it writes.
//
//	w, err := writer.Open(ctx, writer.Config{
//		Archive:  archive,  // an s3store.Store on the Object-Locked bucket
//		Profiles: profiles, // preset.ParseDeployment(doc) then Compose
//		Keys:     provider, // keys.NewTransit or keys.NewLocal
//		Self:     "workload:my-app",
//	})
//	defer w.Close(ctx)
//	emitter, err := emit.New(emit.Options{Source: "my-app", Catalogue: c, Sink: w})
package writer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/truvity/audit/auth"
	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/index"
	"github.com/truvity/audit/index/postgres"
	"github.com/truvity/audit/index/s3scan"
	"github.com/truvity/audit/internal/hold"
	"github.com/truvity/audit/internal/identity"
	"github.com/truvity/audit/internal/registry"
	"github.com/truvity/audit/internal/telemetry"
	inner "github.com/truvity/audit/internal/writer"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
	"github.com/truvity/audit/store"
)

// Config is what a writer needs. Archive, Profiles and Keys are required;
// everything else has a default that is safe for one instance.
type Config struct {
	// Archive is where the copies go: the Object-Locked bucket.
	Archive store.Store
	// Profiles are the deployment's profiles, composed from the presets —
	// preset.ParseDeployment and Compose read the same document the chart
	// renders.
	Profiles map[string]*preset.Profile
	// Keys pseudonymise identifiers. With a provider that can also seal
	// (keys.Transit, keys.Local), the identity behind each pseudonym is kept
	// sealed under the same key, for resolve; see ForgetIdentities.
	Keys keys.Provider
	// Catalogues are the application's own. The common catalogue, which
	// describes the writer's own actions, is always registered.
	Catalogues []*catalogue.Catalogue

	// Database, when given, is the index and the deduplication table shared
	// by every replica, and where catalogues registered through the registry
	// service are read from. Without it the writer indexes nothing and
	// deduplicates in process, and so may run as one instance only.
	Database *pgxpool.Pool
	// Replicas is how many writers share one stream of records. Above one it
	// needs Database, and keys every replica sees the same way.
	Replicas int

	// Self is this writer's own name as an observer, stamped on records that
	// reach it in process — an embedding application's emitter is the
	// application itself, so this names it. A record that arrives over HTTP
	// carries the caller the auth middleware verified instead.
	Self string
	// ForgetIdentities keeps no sealed identities, which makes resolve
	// impossible for everything this writer writes.
	ForgetIdentities bool
	// Instance names this writer in object keys and in its own records.
	// Default: the host name and process id.
	Instance string
	// RollInterval is how long an object stays open within a batch. Default
	// 5 minutes; every batch is flushed before it is acknowledged regardless.
	RollInterval time.Duration
	// Version names this build in the writer's own records.
	Version string

	// Logger, default slog.Default().
	Logger *slog.Logger
	// Meter is where the writer's counters go, default the global provider.
	// The one to alert on is audit.writer.index.deferred; see the runbook.
	Meter metric.MeterProvider
}

// Writer is an open writer. It is a sink.Sink: hand it to an emitter, or serve
// it over HTTP with Handler.
type Writer struct {
	inner   *inner.Writer
	stop    context.CancelFunc
	watcher sync.WaitGroup
	closed  sync.Once
	closeEr error
}

// Open checks the configuration, reads the legal holds, records each profile's
// composition, and returns a writer ready to take records.
//
// It refuses rather than degrades: a writer that could not read the holds, or
// could not record a change of profile, or whose replicas would pseudonymise
// the same person differently, does not start.
func Open(ctx context.Context, c Config) (*Writer, error) {
	switch {
	case c.Archive == nil:
		return nil, errors.New("writer: an archive is required")
	case len(c.Profiles) == 0:
		return nil, errors.New("writer: at least one profile is required: a writer with none keeps nothing")
	case c.Keys == nil:
		return nil, errors.New(
			"writer: a key provider is required: without keys a profile that pseudonymises " +
				"would write identifiers in clear")
	}
	log := c.Logger
	if log == nil {
		log = slog.Default()
	}
	meter := c.Meter
	if meter == nil {
		meter = otel.GetMeterProvider()
	}
	replicas := c.Replicas
	if replicas <= 0 {
		replicas = 1
	}
	instance := c.Instance
	if instance == "" {
		instance = record.InstanceName()
	}

	common, err := catalogue.Common()
	if err != nil {
		return nil, err
	}
	local := &inner.Registry{}
	local.Register(common)
	for _, cat := range c.Catalogues {
		local.Register(cat)
	}

	dedupe := inner.Dedupe(&inner.MemoryDedupe{})
	var indexer index.Indexer
	var shared *registry.Registry
	if c.Database != nil {
		// A writer whose database is at another schema version refuses to
		// start. Migrating is a step an operator takes, not something several
		// replicas race each other to do.
		if err := postgres.CheckVersion(ctx, c.Database); err != nil {
			return nil, err
		}
		if indexer, err = postgres.New(c.Database); err != nil {
			return nil, err
		}
		shared = &registry.Registry{Store: registry.Postgres{DB: c.Database}}
		if dedupe, err = postgres.NewDedupe(c.Database, longestDedupe(c.Profiles)); err != nil {
			return nil, err
		}
		// Every writer of a deployment must hold the same key directory: one
		// with its own mints its own keys, and the same person gets a second
		// pseudonym on it. The database is the one place all replicas can
		// compare, so each binds its directory there and one that brings
		// another is refused.
		if l, ok := c.Keys.(*keys.Local); ok && l.Dir != "" {
			id, err := l.DirectoryID()
			if err != nil {
				return nil, err
			}
			if err := postgres.BindKeyDirectory(ctx, c.Database, id); err != nil {
				return nil, err
			}
		}
	}
	if err := inner.GuardReplicas(replicas, dedupe); err != nil {
		return nil, err
	}
	if err := inner.GuardKeys(c.Profiles, c.Keys != nil); err != nil {
		return nil, err
	}
	if err := inner.GuardHashes(c.Catalogues, c.Keys != nil); err != nil {
		return nil, err
	}
	if l, ok := c.Keys.(*keys.Local); ok && replicas > 1 && l.Dir == "" {
		return nil, errors.New(
			"writer: more than one replica with keys held only in memory: each replica would mint " +
				"its own keys and the same person would get a different pseudonym on each")
	}

	longest := longestRetention(c.Profiles)
	keep := func(at time.Time) time.Time { return at.Add(longest) }

	// Legal holds. A hold is placed on a prefix and objects keep arriving under
	// it, so the writer has to know: an object held only by a later sweep was
	// deletable in between, which is the window the hold exists to close.
	holds := &hold.Watcher{Holds: hold.Store{Store: c.Archive, RetainUntil: keep}, Every: time.Minute}
	// Read once before anything is written. After that, a failed refresh
	// keeps the last answer: forgetting a hold is worse than acting on a list
	// a minute old.
	if err := holds.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("writer: reading the legal holds: %w", err)
	}

	counts, err := telemetry.NewWriter(meter)
	if err != nil {
		return nil, err
	}

	// The way back from a pseudonym, sealed under the same key, for resolve.
	var identities inner.Remembering
	if sealer, ok := c.Keys.(keys.Sealer); ok && !c.ForgetIdentities {
		identities = &identity.Map{Store: c.Archive, Keys: sealer, RetainUntil: keep}
	}

	// An addendum names earlier records by id. The index answers where each
	// is directly; without one, a scan of the archive looks within its budget.
	var records inner.Locator = &s3scan.Scanner{Store: c.Archive}
	if located, ok := indexer.(inner.Locator); ok {
		records = located
	}

	self := c.Self
	w, err := inner.New(&inner.Writer{
		Identity: func(ctx context.Context) string {
			if subject := auth.SubjectFrom(ctx); subject != "" {
				return subject
			}
			return self
		},
		Records:    records,
		Catalogues: resolver{local: local, shared: shared},
		Splitter:   &inner.Splitter{Profiles: c.Profiles, Keys: c.Keys, Identities: identities},
		Roller: &inner.Roller{
			Store: c.Archive, Instance: instance, Interval: c.RollInterval,
			Indexer: indexer, Held: holds.Held,
			OnPut: func(key string, n int) {
				log.Info("object written", "key", key, "records", n)
				counts.Written(key, n)
			},
			// The object is durable and the records are safe; what is behind
			// is the index, which a reindex of that day repairs.
			OnIndexDeferred: func(key string, rows int, err error) {
				log.Error("object written but not indexed", "key", key, "rows", rows, "error", err,
					"repair", "audit reindex --profile <name> --from <day> --to <day>")
				counts.IndexDeferred(key, rows)
			},
		},
		Dedupe:     dedupe,
		DeadLetter: &inner.StoreDeadLetter{Store: c.Archive, Instance: instance, RetainUntil: keep},
		// What describes records outlives the longest of them.
		Archive: &inner.SchemaArchive{Store: c.Archive, RetainUntil: keep},
		Meta:    common,
		Version: c.Version,
		Hooks: inner.Hooks{
			OnDeadLettered: func(r *record.Record, reason string) {
				log.Error("dead letter", "id", r.GetId(), "action", r.GetAction(), "reason", reason)
				counts.DeadLettered()
			},
			OnDuplicatesLikely: func(ids []string, err error) {
				log.Warn("records written but not marked; a redelivery will be written again",
					"records", len(ids), "error", err)
				counts.DuplicatesLikely(len(ids))
			},
			OnUnhandled: func(action string, p []string) {
				log.Warn("no configured profile keeps these", "action", action, "profiles", p)
			},
			OnMetaDropped: func(action, reason string) {
				log.Error("the writer could not record itself", "action", action, "reason", reason)
				counts.MetaDropped()
			},
			OnRetentionNotExtended: func(profile, id string, err error) {
				log.Error("an addendum could not lengthen the lock on an earlier record",
					"profile", profile, "record", id, "error", err)
				counts.RetentionNotExtended(profile)
			},
		},
	})
	if err != nil {
		return nil, err
	}

	// Before the first record: what each profile keeps now, and whether that
	// changed since the last composition recorded. A writer that cannot
	// record a change does not start, since every record it wrote would mean
	// something the trail does not say.
	presets, err := preset.Builtin()
	if err != nil {
		return nil, err
	}
	versions := make(map[string]string, len(presets))
	for name, p := range presets {
		versions[name] = p.Version
	}
	if err := w.RecordCompositions(ctx, c.Profiles, versions); err != nil {
		_ = w.Close(context.Background())
		return nil, err
	}

	out := &Writer{inner: w}
	watching, stop := context.WithCancel(context.Background())
	out.stop = stop
	out.watcher.Add(1)
	go func() {
		defer out.watcher.Done()
		holds.Run(watching, func(err error) {
			log.Error("could not refresh the legal holds; keeping the last answer", "error", err)
		})
	}()

	// Said once the writer is built, so that the record means what a reader
	// will take it to mean.
	w.Started(ctx)
	w.Registered(ctx, common)
	for _, cat := range c.Catalogues {
		w.Registered(ctx, cat)
	}
	return out, nil
}

// Write implements sink.Sink. It returns only once every record in the batch
// is in the archive, seen before, or dead-lettered.
func (w *Writer) Write(ctx context.Context, req *sink.Request) (*sink.Result, error) {
	return w.inner.Write(ctx, req)
}

// Handler serves the writer as the sink's Connect service, for emitters in
// other processes. With an authenticator, every caller must present a token
// it verifies, and the verified subject is stamped as the record's observer;
// nil accepts anybody, under no name.
func (w *Writer) Handler(a auth.Authenticator) (string, http.Handler) {
	path, handler := sink.NewHandler(w)
	if a != nil {
		handler = auth.Middleware(a, handler)
	}
	return path, handler
}

// Close records that the writer stopped, writes whatever is still gathered,
// and stops watching the legal holds. Nothing is taken after it.
func (w *Writer) Close(ctx context.Context) error {
	w.closed.Do(func() {
		w.closeEr = w.inner.Close(ctx)
		w.stop()
		w.watcher.Wait()
	})
	return w.closeEr
}

// resolver answers the writer's question about a record's catalogue: those
// given in the configuration first, then whatever was registered in the
// database — on hand first, because that is what a deployment with no
// registry has, and what an operator repairing a bad registration reaches for.
type resolver struct {
	local  *inner.Registry
	shared *registry.Registry
}

func (r resolver) Get(ctx context.Context, source, version string) (*catalogue.Catalogue, error) {
	c, err := r.local.Get(ctx, source, version)
	if err == nil || r.shared == nil {
		return c, err
	}
	return r.shared.Get(ctx, source, version)
}

// longestDedupe is how long a written identifier is remembered: the widest
// window any profile's presets ask for, since one table serves them all.
func longestDedupe(profiles map[string]*preset.Profile) time.Duration {
	var longest time.Duration
	for _, p := range profiles {
		if d := time.Duration(p.Pipeline.DedupeWindowDays) * 24 * time.Hour; d > longest {
			longest = d
		}
	}
	return longest
}

// longestRetention is how long the longest-lived profile keeps a copy, which
// is what anything that has to outlive every record is kept for.
func longestRetention(profiles map[string]*preset.Profile) time.Duration {
	now := time.Now().UTC()
	var longest time.Duration
	for _, p := range profiles {
		if d := p.RetainUntil(now, nil).Sub(now); d > longest {
			longest = d
		}
	}
	if longest == 0 {
		longest = 10 * 365 * 24 * time.Hour
	}
	return longest
}
