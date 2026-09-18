// Command audit-writer takes records and puts the copies their profiles keep.
//
// It is the one component that holds the pseudonymisation keys and the only one
// that writes to the archive. Everything else either hands it records or reads
// what it wrote.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"

	"github.com/truvity/audit/auth"
	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/index"
	"github.com/truvity/audit/index/postgres"
	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/internal/hold"
	"github.com/truvity/audit/internal/registry"
	"github.com/truvity/audit/internal/telemetry"
	"github.com/truvity/audit/internal/writer"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
	"github.com/truvity/audit/sink/natssink"
	"github.com/truvity/audit/store"
	"github.com/truvity/audit/store/s3store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("audit-writer", "error", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		bucket     = flag.String("bucket", env("AUDIT_BUCKET", ""), "the bucket the archive is in")
		prefix     = flag.String("prefix", env("AUDIT_PREFIX", ""), "the prefix within the bucket")
		kmsKey     = flag.String("kms-key", env("AUDIT_KMS_KEY", ""), "the key objects are encrypted with")
		governance = flag.Bool("governance", false,
			"write the weaker lock mode; for a non-production bucket where a mistake has to be undoable")
		deployment = flag.String("deployment", env("AUDIT_DEPLOYMENT", ""), "the profile configuration")
		catalogues = flag.String("catalogues", env("AUDIT_CATALOGUES", ""), "a directory of catalogues to register")
		keyRoot    = flag.String("key-root", env("AUDIT_KEY_ROOT", ""), "file holding the 32-byte root the data keys are wrapped under")
		keyDir     = flag.String("key-dir", env("AUDIT_KEY_DIR", ""), "where wrapped data keys are kept")
		replicas   = flag.Int("replicas", envInt("AUDIT_REPLICAS", 1), "how many writers share this stream")
		database   = flag.String("database", env("AUDIT_DATABASE", ""),
			"the Postgres URL of the index; without it the writer indexes nothing and deduplicates in process")
		listen    = flag.String("listen", env("AUDIT_LISTEN", ":8080"), "address to serve the sink on")
		workloads = flag.String("workloads", env("AUDIT_WORKLOADS", ""),
			"the file naming the issuers trusted to say which workload is publishing")
		anonymous = flag.Bool("anonymous-writes", false,
			"accept writes over HTTP from callers nobody verified; for a trial install only")
		streamURL = flag.String("stream-url", env("AUDIT_STREAM_URL", ""),
			"the NATS server holding the wide stream; without it the writer only serves the sink")
		streamName   = flag.String("stream", env("AUDIT_STREAM", "AUDIT"), "the stream to consume")
		consumerName = flag.String("consumer", env("AUDIT_CONSUMER", "audit-writer"),
			"the durable consumer this deployment's writers share")
		streamBatch = flag.Int("stream-batch", envInt("AUDIT_STREAM_BATCH", 100),
			"how many records are taken from the stream at once")
		streamAckWait = flag.Duration("stream-ack-wait", 30*time.Second,
			"how long the stream waits for the writer to take a batch before offering it again")
		rollEvery = flag.Duration("roll-interval", 5*time.Minute, "how long an object stays open")
		version   = flag.String("version", env("AUDIT_VERSION", "dev"), "this build's version")
	)
	flag.Parse()

	switch {
	case *bucket == "":
		return errors.New("name the archive's bucket with --bucket")
	case *deployment == "":
		return errors.New("give the profile configuration with --deployment")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	presets, err := preset.Builtin()
	if err != nil {
		return err
	}
	d, err := cli.LoadDeployment(*deployment)
	if err != nil {
		return err
	}
	profiles, err := d.Compose(presets)
	if err != nil {
		return err
	}

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return err
	}
	archive, err := s3store.FromConfig(cfg, s3store.Options{
		Bucket: *bucket, Prefix: *prefix, KMSKeyID: *kmsKey, Governance: *governance,
	})
	if err != nil {
		return err
	}

	provider, err := keyProvider(*keyRoot, *keyDir)
	if err != nil {
		return err
	}
	defer provider.Close() //nolint:errcheck // shutting down

	// The common catalogue is registered whether or not a deployment brought
	// any of its own: it is what describes the writer's own actions, and the
	// writer resolves those through the registry like anyone else's.
	common, err := catalogue.Common()
	if err != nil {
		return err
	}
	local := &writer.Registry{}
	local.Register(common)
	registered := []*catalogue.Catalogue{common}
	if *catalogues != "" {
		found, err := registerAll(local, *catalogues)
		if err != nil {
			return err
		}
		registered = append(registered, found...)
	}

	// The index and the deduplication table live in the same database on
	// purpose: both sit on the write path, and it is having them that turns
	// the writer from a single instance into a deployment. Without a database
	// the writer still writes the archive, which is the part that is evidence.
	dedupe := writer.Dedupe(&writer.MemoryDedupe{})
	var indexer index.Indexer
	var shared *registry.Registry
	if *database != "" {
		pool, err := pgxpool.New(ctx, *database)
		if err != nil {
			return err
		}
		defer pool.Close()
		// A writer whose database is at another schema version refuses to
		// start. Migrating is a step an operator takes, not something several
		// replicas race each other to do.
		if err := postgres.CheckVersion(ctx, pool); err != nil {
			return err
		}
		if indexer, err = postgres.New(pool); err != nil {
			return err
		}
		// Catalogues registered through the registry service live in the same
		// database, so the writer reads them from it rather than through
		// another hop. One given on disk still wins: that is what a deployment
		// without a registry has, and what an operator reaches for when the
		// registry is the thing that is broken.
		shared = &registry.Registry{Store: registry.Postgres{DB: pool}}
		if dedupe, err = postgres.NewDedupe(pool, longestDedupe(profiles)); err != nil {
			return err
		}
		// Every writer of this deployment must hold the same key directory:
		// one with its own mints its own keys, and the same person gets a
		// second pseudonym on it. So does every tenant at once when the
		// directory is lost and recreated. The database is the one place all
		// replicas can compare, so each binds its directory there on start and
		// one that brings another is refused.
		if local, ok := provider.(*keys.Local); ok && local.Dir != "" {
			id, err := local.DirectoryID()
			if err != nil {
				return err
			}
			if err := postgres.BindKeyDirectory(ctx, pool, id); err != nil {
				return err
			}
		}
	}
	if err := writer.GuardReplicas(*replicas, dedupe); err != nil {
		return err
	}
	if *replicas > 1 && *keyDir == "" {
		return errors.New(
			"writer: more than one replica with keys held only in memory: each replica would mint " +
				"its own keys and the same person would get a different pseudonym on each. Give " +
				"--key-dir on storage every replica shares")
	}

	longest := longestRetention(profiles)
	instance := record.InstanceName()

	// Legal holds. A hold is placed on a prefix and objects keep arriving under
	// it, so the writer has to know: an object held only by a later sweep was
	// deletable in between, which is the window the hold exists to close.
	holds := &hold.Watcher{
		Holds: hold.Store{
			Store:       archive,
			RetainUntil: func(at time.Time) time.Time { return at.Add(longest) },
		},
		Every: time.Minute,
	}
	// Read once before anything is written, and refuse to start otherwise. A
	// writer that began with holds it had not read would write objects under a
	// held prefix without the hold, and the whole point of reading them is that
	// this never happens. After that first read, a failed refresh keeps the last
	// answer: forgetting a hold is worse than acting on a list a minute old.
	if err := holds.Refresh(ctx); err != nil {
		return fmt.Errorf("writer: reading the legal holds: %w", err)
	}
	go holds.Run(ctx, func(err error) {
		slog.Error("could not refresh the legal holds; keeping the last answer", "error", err)
	})
	// Who is publishing is verified, not declared: the writer stamps the
	// caller's service account as the record's observer, so a record written by
	// the wrong workload names the workload that wrote it. Without a way to
	// verify, a writer reachable over HTTP would take anybody's records under
	// nobody's name — which it does only when told to, for a trial.
	var authenticated auth.Authenticator
	switch {
	case *workloads != "":
		callers, err := cli.LoadWorkloads(*workloads)
		if err != nil {
			return err
		}
		if authenticated, err = auth.NewJWT(ctx, callers.Issuers, slog.Default()); err != nil {
			return err
		}
	case *anonymous:
		slog.Warn("accepting writes from callers nobody verified: records written over HTTP " +
			"carry no observer identity, and anyone who can reach this port can write them")
	default:
		return errors.New(
			"give --workloads so the writer can verify who publishes, or --anonymous-writes " +
				"for a trial install that accepts anybody")
	}

	// Metrics, pushed over OTLP when a collector is named in the environment
	// and a no-op otherwise. The one to alert on is index.deferred.
	stopTelemetry, err := telemetry.Start(ctx, "audit-writer", *version, slog.Default())
	if err != nil {
		return err
	}
	defer stopTelemetry(context.Background()) //nolint:errcheck // shutting down
	counts, err := telemetry.NewWriter(otel.GetMeterProvider())
	if err != nil {
		return err
	}

	w, err := writer.New(&writer.Writer{
		Identity:   auth.SubjectFrom,
		Catalogues: resolver{local: local, shared: shared},
		Splitter:   &writer.Splitter{Profiles: profiles, Keys: provider},
		Roller: &writer.Roller{
			Store: archive, Instance: instance, Interval: *rollEvery,
			Indexer: indexer, Held: holds.Held,
			OnPut: func(key string, records int) {
				slog.Info("object written", "key", key, "records", records)
				counts.Written(key, records)
			},
			// The object is durable and the records are safe; what is behind is
			// the projection, which a reindex of that day repairs. A deployment
			// alerts on this, because an index nobody notices is behind is one
			// that quietly answers wrongly.
			OnIndexDeferred: func(key string, rows int, err error) {
				slog.Error("object written but not indexed",
					"key", key, "rows", rows, "error", err,
					"repair", "audit reindex --profile <name> --from <day> --to <day>")
				counts.IndexDeferred(key, rows)
			},
		},
		Dedupe: dedupe,
		DeadLetter: &writer.StoreDeadLetter{
			Store: archive, Instance: instance,
			RetainUntil: func(at time.Time) time.Time { return at.Add(longest) },
		},
		Archive: &writer.SchemaArchive{
			Store: archive,
			// A record whose schema has been deleted is a record nobody can
			// read, so what describes records outlives the longest of them.
			RetainUntil: func(at time.Time) time.Time { return at.Add(longest) },
		},
		// The writer keeps an account of itself in the archive it writes, so
		// that a reader can tell a quiet hour from a stopped writer.
		Meta:    common,
		Version: *version,
		Hooks: writer.Hooks{
			OnDeadLettered: func(r *record.Record, reason string) {
				slog.Error("dead letter", "id", r.GetId(), "action", r.GetAction(), "reason", reason)
				counts.DeadLettered()
			},
			OnDuplicatesLikely: func(ids []string, err error) {
				slog.Warn("records written but not marked; a redelivery will be written again",
					"records", len(ids), "error", err)
				counts.DuplicatesLikely(len(ids))
			},
			OnUnhandled: func(action string, p []string) {
				slog.Warn("no configured profile keeps these", "action", action, "profiles", p)
			},
			OnMetaDropped: func(action, reason string) {
				slog.Error("the writer could not record itself", "action", action, "reason", reason)
				counts.MetaDropped()
			},
		},
	})
	if err != nil {
		return err
	}
	defer w.Close(context.Background()) //nolint:errcheck // shutting down

	// The stream, when there is one. An application that publishes straight to
	// the writer needs none; a deployment with a stream wants the writer behind
	// a durable consumer, so that a writer that is down is a backlog rather
	// than a hole.
	if *streamURL != "" {
		stop, err := consume(ctx, streamOptions{
			URL: *streamURL, Stream: *streamName, Durable: *consumerName,
			Batch: *streamBatch, AckWait: *streamAckWait,
		}, w)
		if err != nil {
			return err
		}
		defer stop()
	}

	path, handler := sink.NewHandler(w)
	if authenticated != nil {
		handler = auth.Middleware(authenticated, handler)
	}
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	mux.HandleFunc("/healthz", func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(http.StatusOK) })
	server := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	go func() {
		<-ctx.Done()
		// The records still gathered are written before the process goes, and
		// the server stops taking new ones first so nothing arrives meanwhile.
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		if err := w.Close(shutdown); err != nil {
			slog.Error("flushing on shutdown", "error", err)
		}
	}()

	// Said once the writer is built and about to serve, so that the record
	// means what a reader will take it to mean.
	w.Started(ctx)
	for _, c := range registered {
		w.Registered(ctx, c)
	}

	slog.Info("audit-writer", "listen", *listen, "bucket", *bucket, "profiles", len(profiles))
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func keyProvider(rootPath, dir string) (keys.Provider, error) {
	if rootPath == "" {
		return nil, errors.New(
			"give the pseudonymisation root with --key-root: without keys a profile that " +
				"pseudonymises would write identifiers in clear, and this refuses to start rather than do that")
	}
	root, err := os.ReadFile(rootPath)
	if err != nil {
		return nil, err
	}
	return keys.NewLocal(root, dir)
}

func registerAll(r *writer.Registry, dir string) ([]*catalogue.Catalogue, error) {
	found, err := cli.FindCatalogues(dir)
	if err != nil {
		return nil, err
	}
	out := make([]*catalogue.Catalogue, 0, len(found))
	for _, path := range found {
		c, err := catalogue.LoadFS(os.DirFS(dirOf(path)), baseOf(path))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		r.Register(c)
		out = append(out, c)
		slog.Info("catalogue registered", "source", c.Source, "version", c.Version)
	}
	return out, nil
}

// longestRetention is how long the longest-lived profile keeps a copy, which is
// what anything that has to outlive every record is kept for.
// longestDedupe is how long a written identifier is remembered.
//
// It is the widest window the deployment's presets ask for, because the table
// is shared by every profile and a record that one profile keeps for a fortnight
// must not be re-written because another profile's window was shorter. The
// window wants to cover the longest redelivery the stream below permits, which
// is what the presets are expressing when they declare it.
func longestDedupe(profiles map[string]*preset.Profile) time.Duration {
	var longest time.Duration
	for _, p := range profiles {
		if d := time.Duration(p.Pipeline.DedupeWindowDays) * 24 * time.Hour; d > longest {
			longest = d
		}
	}
	return longest
}

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

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func envInt(name string, fallback int) int {
	var v int
	if _, err := fmt.Sscanf(os.Getenv(name), "%d", &v); err == nil && v > 0 {
		return v
	}
	return fallback
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}

func baseOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

var _ store.Store = (*s3store.Store)(nil)

// streamOptions are how this writer reads the wide stream.
type streamOptions struct {
	URL, Stream, Durable string
	Batch                int
	// AckWait is how long the stream waits for a batch to be taken before
	// offering it again. It has to be longer than the longest a write can
	// honestly take — a batch is acknowledged only once its records are in the
	// archive, and that is a put to object storage — or the stream will offer
	// the same records to a second replica while the first is still writing
	// them, and the deduplication table will earn its keep for no reason.
	AckWait time.Duration
}

// consume binds a durable pull consumer to the writer and runs it until the
// context is cancelled. The returned function waits for it to stop.
//
// The consumer is durable and shared by every replica, which is what makes a
// second replica a second pair of hands rather than a second copy of every
// record. A batch is acknowledged only once the writer has taken it, so a
// writer that cannot write leaves its messages for the redelivery rather than
// losing them, which is the whole reason the stream is there.
func consume(ctx context.Context, o streamOptions, target sink.Sink) (func(), error) {
	conn, err := nats.Connect(o.URL,
		nats.Name("audit-writer"),
		// Reconnect for as long as the process lives. A writer that gave up on
		// the stream would go on answering its own health check while the
		// backlog grew behind it.
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				slog.Error("disconnected from the stream", "error", err)
			}
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			slog.Info("reconnected to the stream", "url", c.ConnectedUrl())
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("writer: connecting to %s: %w", o.URL, err)
	}

	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("writer: %w", err)
	}
	found, err := js.Stream(ctx, o.Stream)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf(
			"writer: stream %q: %w; the stream is the deployment's to create, not this writer's, "+
				"because its retention and its discard policy decide whether a full stream "+
				"refuses publishers or drops records", o.Stream, err)
	}
	// The consumer is created here because its acknowledgement policy is a
	// property of what the writer promises: every message acknowledged
	// explicitly, only once the records are in the archive.
	//
	// MaxDeliver is left unlimited. A record must not fall out of the stream
	// because the writer was unable to take it a few times, and nothing here
	// loops forever on a bad record: a record the writer cannot process is
	// accepted and dead-lettered, so a batch fails only on a fault that will
	// pass.
	jc, err := found.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       o.Durable,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       o.AckWait,
		MaxAckPending: o.Batch * 2,
	})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("writer: consumer %q on stream %q: %w", o.Durable, o.Stream, err)
	}

	consumer, err := natssink.NewConsumer(jc, target, natssink.ConsumerOptions{
		Batch: o.Batch,
		OnError: func(err error) {
			// The batch is not acknowledged, so the stream brings it back after
			// AckWait. Saying so is the only way a deployment learns that
			// records are going round rather than through.
			slog.Error("the writer refused a batch from the stream", "error", err)
		},
	})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("writer: %w", err)
	}

	running, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := consumer.Run(running); err != nil {
			slog.Error("the stream consumer stopped", "error", err)
		}
	}()
	slog.Info("consuming the stream", "url", o.URL, "stream", o.Stream, "consumer", o.Durable)

	return func() {
		// Stop fetching, wait for the batch in hand, then let the connection
		// go. Closing underneath a running fetch is what produces a shelf of
		// alarming errors on an orderly shutdown.
		cancel()
		<-done
		_ = conn.Drain()
	}, nil
}

// resolver answers the writer's question about a record's catalogue: on disk
// first, then whatever the deployment registered.
//
// On disk first because that is what a deployment with no registry has at all,
// and because an operator repairing a bad registration needs a way to put the
// right document in front of the writer without going through the thing that
// took the wrong one.
type resolver struct {
	local  *writer.Registry
	shared *registry.Registry
}

func (r resolver) Get(ctx context.Context, source, version string) (*catalogue.Catalogue, error) {
	c, err := r.local.Get(ctx, source, version)
	if err == nil {
		return c, nil
	}
	if r.shared == nil {
		return nil, err
	}
	return r.shared.Get(ctx, source, version)
}
