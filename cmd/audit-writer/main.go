// Command audit-writer is an installation's front door and its write path,
// which are one process in a small installation and two in a busy one.
//
// With --mode writer, the default, it does both: it serves the sink, puts the
// copies their profiles keep, and consumes a stream when one is configured.
// With --mode receiver it only takes records and publishes them to the stream,
// holding no bucket and no keys; the writers consume the other end. That split
// is what lets the write path scale away from the front door, and what keeps
// the stream's credentials out of the application entirely.
//
// It takes records and puts the copies their profiles keep, and it accepts the
// application's catalogue at start-up (RegisterCatalogue) — an installation
// belongs to one application, so a registry service of its own would be a
// Deployment for a single call. It is the only component that writes to the
// archive, and the only one that holds the pseudonymisation keys where a
// deployment configures any. Everything else either hands it records or reads
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

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/truvity/audit/auth"
	"github.com/truvity/audit/catalogue"
	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/internal/registry"
	"github.com/truvity/audit/internal/telemetry"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
	"github.com/truvity/audit/sink/natssink"
	"github.com/truvity/audit/store"
	"github.com/truvity/audit/store/s3store"
	"github.com/truvity/audit/writer"
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
		replicas   = flag.Int("replicas", envInt("AUDIT_REPLICAS", 1), "how many writers share this stream")
		database   = flag.String("database", env("AUDIT_DATABASE", ""),
			"the Postgres URL of the index; without it the writer indexes nothing and deduplicates in process")
		listen    = flag.String("listen", env("AUDIT_LISTEN", ":8080"), "address to serve the sink on")
		workloads = flag.String("workloads", env("AUDIT_WORKLOADS", ""),
			"the file naming the issuers trusted to say which workload is publishing")
		keepIdentities = flag.Bool("keep-identities", true,
			"keep the identity behind each pseudonym, sealed under its key, so that resolve can find it")
		anonymous = flag.Bool("anonymous-writes", false,
			"accept writes over HTTP from callers nobody verified; for a trial install only")
		streamURL = flag.String("stream-url", env("AUDIT_STREAM_URL", ""),
			"the NATS server holding the wide stream; without it the writer only serves the sink")
		streamName  = flag.String("stream", env("AUDIT_STREAM", "AUDIT"), "the stream to consume")
		streamToken = flag.String("stream-token-file", env("AUDIT_STREAM_TOKEN_FILE", ""),
			"a file holding the token presented to the stream's broker, read afresh on every "+
				"connect; a projected service-account token where the broker verifies workloads. "+
				"Empty connects with no credentials")
		consumerName = flag.String("consumer", env("AUDIT_CONSUMER", "audit-writer"),
			"the durable consumer this deployment's writers share")
		streamBatch = flag.Int("stream-batch", envInt("AUDIT_STREAM_BATCH", 100),
			"how many records are taken from the stream at once")
		streamAckWait = flag.Duration("stream-ack-wait", 2*time.Minute,
			"how long the stream waits for the writer to take a batch before offering it again. It "+
				"must exceed --roll-interval plus the longest a put can take, or the stream will "+
				"offer records the consumer is still gathering")
		rollEvery = flag.Duration("roll-interval", 30*time.Second,
			"how long records gathered from the stream wait before they are written, and how long "+
				"an object stays open within one write")
		rollRecords = flag.Int("roll-max-records", envInt("AUDIT_ROLL_MAX_RECORDS", 5000),
			"how many records gathered from the stream are written at once")
		version = flag.String("version", env("AUDIT_VERSION", "dev"), "this build's version")
		mode    = flag.String("mode", env("AUDIT_MODE", "writer"),
			"writer (serve the sink, write the archive, consume the stream) or "+
				"receiver (serve the sink and publish to the stream, nothing else)")
	)
	keyFlags := cli.NewKeyFlags(flag.CommandLine, env)
	flag.Parse()

	switch *mode {
	case "writer":
		if *bucket == "" {
			return errors.New("name the archive's bucket with --bucket")
		}
	case "receiver":
		// A receiver is the front door and nothing else. Letting it hold the
		// archive's credentials or a key would make it a writer that happens
		// to publish, and the separation is the point: see
		// docs/decisions/0011-one-installation-per-service-or-product.md.
		switch {
		case *streamURL == "":
			return errors.New("--mode receiver needs --stream-url: a receiver publishes, and without a stream there is nowhere to publish to")
		case *bucket != "":
			return errors.New("--mode receiver takes no --bucket: the writers on the other side of the stream hold the archive")
		case keyFlags.Configured():
			return errors.New("--mode receiver takes no key provider: pseudonyms are the writer's, and a receiver that held the keys would be one")
		}
	default:
		return fmt.Errorf("--mode is writer or receiver, not %q", *mode)
	}
	if *deployment == "" {
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

	var archive store.Store
	var provider keys.Provider
	if *mode == "writer" {
		cfg, err := config.LoadDefaultConfig(ctx)
		if err != nil {
			return err
		}
		if archive, err = s3store.FromConfig(cfg, s3store.Options{
			Bucket: *bucket, Prefix: *prefix, KMSKeyID: *kmsKey, Governance: *governance,
		}); err != nil {
			return err
		}
		if provider, err = keyFlags.Open(ctx); err != nil {
			return err
		}
	}
	if provider != nil {
		defer provider.Close() //nolint:errcheck // shutting down
	}

	var found []*catalogue.Catalogue
	if *catalogues != "" {
		if found, err = loadAll(*catalogues); err != nil {
			return err
		}
	}

	// The index and the deduplication table live in the same database on
	// purpose: both sit on the write path, and it is having them that turns
	// the writer from a single instance into a deployment. Without a database
	// the writer still writes the archive, which is the part that is evidence.
	var pool *pgxpool.Pool
	if *database != "" {
		if pool, err = pgxpool.New(ctx, *database); err != nil {
			return err
		}
		defer pool.Close()
	}
	if *replicas > 1 && keyFlags.Local() && *keyFlags.Dir == "" {
		return errors.New(
			"writer: more than one replica with keys held only in memory: each replica would mint " +
				"its own keys and the same person would get a different pseudonym on each. Give " +
				"--key-dir on storage every replica shares")
	}

	// Who is publishing is verified, not declared: the writer stamps the
	// caller's service account as the record's observer, so a record written by
	// the wrong workload names the workload that wrote it. Without a way to
	// verify, a writer reachable over HTTP would take anybody's records under
	// nobody's name — which it does only when told to, for a trial.
	var authenticated auth.Authenticator
	// Whose catalogue a document is, is never the document's to claim: it comes
	// from the caller's verified service account, mapped to a source in the
	// workloads file. An empty mapping answers "" for everybody, which refuses
	// every registration — the right answer for a deployment that never said
	// who may register what. It must never be nil: the registry reads a nil
	// Identity as "nobody is checking" and would then take the document's word.
	sourceOf := auth.Workloads(nil).SourceFrom
	switch {
	case *workloads != "":
		callers, err := cli.LoadWorkloads(*workloads)
		if err != nil {
			return err
		}
		if authenticated, err = auth.NewJWT(ctx, callers.Issuers, slog.Default()); err != nil {
			return err
		}
		sourceOf = callers.Map.SourceFrom
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

	// What this process is: the write path, or the front door in front of a
	// stream. Both serve the same sink on the same port, so an application is
	// configured the same way either side of the choice.
	var (
		front    sink.Sink
		shutdown func(context.Context) error
	)
	if *mode == "receiver" {
		publisher, stop, err := publisherFor(ctx, streamOptions{
			URL: *streamURL, TokenFile: *streamToken, Stream: *streamName, Durable: *consumerName,
			Batch: *streamBatch, AckWait: *streamAckWait,
		})
		if err != nil {
			return err
		}
		defer stop()
		// The receiver stamps before it publishes. The writers on the other
		// side are reading messages and have no caller to verify, so an
		// identity not attached here is an identity lost.
		front = &sink.Receiver{
			To: publisher, Version: *version, Instance: record.InstanceName(),
		}
		shutdown = func(context.Context) error { return nil }
	} else {
		w, err := writer.Open(ctx, writer.Config{
			Archive:          archive,
			Profiles:         profiles,
			Keys:             provider,
			Catalogues:       found,
			Database:         pool,
			Replicas:         *replicas,
			ForgetIdentities: !*keepIdentities,
			RollInterval:     *rollEvery,
			Version:          *version,
			// Records reaching this writer over the stream were stamped by a
			// receiver of this installation, which is the only thing that may
			// publish to it.
			FromStream: *streamURL != "",
		})
		if err != nil {
			return err
		}
		defer w.Close(context.Background()) //nolint:errcheck // shutting down
		front, shutdown = w, w.Close

		// The stream, when there is one. An application that publishes straight
		// to the writer needs none; a deployment with a stream wants the writer
		// behind a durable consumer, so that a writer that is down is a backlog
		// rather than a hole.
		if *streamURL != "" {
			stop, err := consume(ctx, streamOptions{
				URL: *streamURL, TokenFile: *streamToken, Stream: *streamName, Durable: *consumerName,
				Batch: *streamBatch, AckWait: *streamAckWait,
				Window: *rollEvery, MaxRecords: *rollRecords,
			}, w)
			if err != nil {
				return err
			}
			defer stop()
		}
	}

	path, handler := sink.NewHandler(front)
	if authenticated != nil {
		handler = auth.Middleware(authenticated, handler)
	}
	mux := http.NewServeMux()
	mux.Handle(path, handler)

	// Catalogue registration is served here, beside the sink. An installation
	// belongs to one application, so a registry of its own would be a
	// Deployment, a ServiceAccount and a network policy for one call at
	// start-up: docs/decisions/0011-one-installation-per-service-or-product.md.
	// It needs the database the index is in, because a registered catalogue
	// lives beside the rows it describes and shares their migration chain.
	if pool != nil {
		common, err := catalogue.Common()
		if err != nil {
			return err
		}
		reg := &registry.Registry{
			Store:    registry.Postgres{DB: pool},
			Profiles: profiles,
			Builtin:  []*catalogue.Catalogue{common},
			Identity: sourceOf,
			Keys:     provider != nil,
			// A gap in coverage is the deployment's to close, not a reason to
			// refuse the application that registered while it was open.
			OnUncovered: func(_ context.Context, profile string, missing []string) {
				slog.Warn("a profile requires categories no registered catalogue emits",
					"profile", profile, "missing", missing)
			},
			// Recorded through this writer itself: a catalogue arriving changes
			// what the archive's records mean, so the archive should say when.
			OnRegistered: recorder(front, *version),
		}
		regPath, regHandler := registry.NewHandler(reg)
		mux.Handle(regPath, auth.Middleware(authenticated, regHandler))
	} else {
		slog.Warn("no database: this writer serves no catalogue registration, " +
			"because a registered catalogue is kept beside the index it describes")
	}

	mux.HandleFunc("/healthz", func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(http.StatusOK) })
	server := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	go func() {
		<-ctx.Done()
		// The records still gathered are written before the process goes, and
		// the server stops taking new ones first so nothing arrives meanwhile.
		stopping, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = server.Shutdown(stopping)
		if err := shutdown(stopping); err != nil {
			slog.Error("flushing on shutdown", "error", err)
		}
	}()

	slog.Info("audit-writer", "mode", *mode, "listen", *listen, "bucket", *bucket, "profiles", len(profiles))
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// recorder records a registration, because a catalogue arriving changes what
// the archive's records mean.
func recorder(to sink.Sink, version string) func(context.Context, registry.Entry) {
	return func(ctx context.Context, e registry.Entry) {
		_, err := to.Write(ctx, &sink.Request{
			Delivery: auditv1.Delivery_DELIVERY_BLOCK,
			Records:  []*record.Record{registrationRecord(e, version)},
		})
		if err != nil {
			// The catalogue is registered and the trail does not say so. A
			// deployment alerts on this: what a record means has changed and
			// there is no event marking when.
			slog.Error("a catalogue was registered and could not be recorded",
				"source", e.Source, "version", e.Version, "error", err)
		}
	}
}

// registrationRecord is the writer's own record of a catalogue arriving. It
// carries the time it happened, which every emitter's record carries and
// this one, being built by hand, once did not: without a time the archive
// keyed it under the epoch, outside every digest window, for as long as the
// lock lasts.
func registrationRecord(e registry.Entry, version string) *record.Record {
	return &record.Record{
		Action:           "audit.catalogue.registered",
		Operation:        auditv1.Operation_OPERATION_CREATE,
		TenantId:         record.TenantPlatform,
		Source:           "audit",
		CatalogueVersion: catalogueVersion(),
		SchemaVersion:    record.SchemaVersion,
		Id:               record.NewID(),
		OccurredAt:       timestamppb.Now(),
		Actor:            &record.Actor{Kind: "service", Id: e.RegisteredBy},
		Observer:         &record.Observer{Version: version, Instance: record.InstanceName()},
		Outcome:          &record.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS},
		Targets: []*record.Target{
			{Type: "catalogue", Id: e.Source + "@" + e.Version},
		},
	}
}

func catalogueVersion() string {
	c, err := catalogue.Common()
	if err != nil {
		return ""
	}
	return c.Version
}

// loadAll reads every catalogue in a directory.
func loadAll(dir string) ([]*catalogue.Catalogue, error) {
	paths, err := cli.FindCatalogues(dir)
	if err != nil {
		return nil, err
	}
	out := make([]*catalogue.Catalogue, 0, len(paths))
	for _, path := range paths {
		c, err := catalogue.LoadFS(os.DirFS(dirOf(path)), baseOf(path))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, c)
		slog.Info("catalogue registered", "source", c.Source, "version", c.Version)
	}
	return out, nil
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
	// TokenFile holds the token presented to the broker, when it verifies who
	// connects; empty connects with no credentials. See connectOptions.
	TokenFile string
	Batch     int
	// AckWait is how long the stream waits for a batch to be taken before
	// offering it again. It has to be longer than the longest a write can
	// honestly take — a batch is acknowledged only once its records are in the
	// archive, and that is a put to object storage — or the stream will offer
	// the same records to a second replica while the first is still writing
	// them, and the deduplication table will earn its keep for no reason.
	AckWait time.Duration
	// Window and MaxRecords are the roll: how much a consumer gathers from the
	// stream before writing it. See natssink.ConsumerOptions.
	Window     time.Duration
	MaxRecords int
}

// publisherFor connects a receiver to the stream it publishes to.
//
// It asks for the stream by name and fails when it is not there, for the same
// reason the consumer does: the stream is the deployment's to create, because
// its retention and its discard policy decide whether a full stream refuses
// publishers or drops records, and neither is this process's to choose.
func publisherFor(ctx context.Context, o streamOptions) (sink.Sink, func(), error) {
	conn, err := nats.Connect(o.URL, connectOptions("audit-receiver", o)...)
	if err != nil {
		return nil, nil, fmt.Errorf("receiver: connecting to %s: %w", o.URL, err)
	}
	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("receiver: %w", err)
	}
	found, err := js.Stream(ctx, o.Stream)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf(
			"receiver: stream %q: %w; the stream is the deployment's to create, not this "+
				"receiver's, because its retention and its discard policy decide whether a full "+
				"stream refuses publishers or drops records", o.Stream, err)
	}
	info, err := found.Info(ctx)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("receiver: stream %q: %w", o.Stream, err)
	}
	if len(info.Config.Subjects) == 0 {
		conn.Close()
		return nil, nil, fmt.Errorf("receiver: stream %q listens on no subject", o.Stream)
	}
	p, err := natssink.NewPublisher(js, natssink.Options{Subject: info.Config.Subjects[0]})
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	slog.Info("publishing to the stream", "stream", o.Stream, "subject", info.Config.Subjects[0])
	return p, conn.Close, nil
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
	conn, err := nats.Connect(o.URL, connectOptions("audit-writer", o)...)
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
		Batch:      o.Batch,
		Window:     o.Window,
		MaxRecords: o.MaxRecords,
		AckWait:    o.AckWait,
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
