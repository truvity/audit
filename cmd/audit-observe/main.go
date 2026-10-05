// Command audit-observe is the indexer: it turns the archive into the search
// index by following the bucket (docs/decisions/0020).
//
// It is a process of its own, and not a mode of audit-query, because the two
// hold opposite database rights. This one writes the index and holds no
// searcher; the query service reads the index as a role that can write nothing,
// bound by row-level security, and parses what callers send it. A process that
// had both would put the index's write credential in the process that faces
// callers, which is the thing the split is for.
//
// It never writes the archive and never sits on the write path: it can be
// absent, paused or replaced, and ingest does not notice.
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

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"

	"github.com/truvity/audit/index/postgres"
	"github.com/truvity/audit/internal/buildinfo"
	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/internal/config"
	readiness "github.com/truvity/audit/internal/health"
	"github.com/truvity/audit/internal/observe"
	"github.com/truvity/audit/internal/telemetry"
	"github.com/truvity/audit/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("audit-observe", "error", err)
		os.Exit(1)
	}
}

func run() error {
	// Declared so that the flag package accepts it; config.Path reads it, with AUDIT_CONFIG.
	flag.String("config", "", "the configuration file, or AUDIT_CONFIG: the one thing that configures this process")
	showVersion := flag.Bool("version", false, "print this build's version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("audit-observe", buildinfo.Version)
		return nil
	}
	configFile, err := config.Path(os.Args[1:], "audit-observe")
	if err != nil {
		return err
	}
	cfg, err := config.LoadObserve(configFile)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	stopTelemetry, err := telemetry.Start(ctx, "audit-observe", buildinfo.Version, slog.Default())
	if err != nil {
		return err
	}
	defer stopTelemetry(context.Background()) //nolint:errcheck // shutting down
	counts, err := telemetry.NewObserve(otel.GetMeterProvider())
	if err != nil {
		return err
	}

	archive, err := cli.OpenArchiveFrom(ctx, cfg.Archive, cfg.SecretReader())
	if err != nil {
		return err
	}
	poolConfig, err := cfg.Database.PoolConfig(ctx, cfg.SecretReader())
	if err != nil {
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	// An indexer whose database is at another schema version refuses to start:
	// migrating is a step an operator takes, not something it races to do.
	if err := postgres.CheckVersion(ctx, pool); err != nil {
		return err
	}
	target, err := postgres.New(pool)
	if err != nil {
		return err
	}

	wake := make(chan struct{}, 1)
	release, err := wakeFrom(ctx, cfg.Wake, wake)
	if err != nil {
		return err
	}
	defer release()

	indexer := &observe.Indexer{
		Store:    archive,
		Cursors:  target,
		Fields:   observe.FieldsFrom(&observe.ArchiveCatalogues{Store: archive}),
		Settle:   cfg.Settle.D(),
		Interval: cfg.Interval.D(),
		Batch:    cfg.Batch,
		Profiles: cfg.Profiles,
		Wake:     wake,
		OnObject: func(profile, _ string, rows int, lag time.Duration) { counts.Indexed(profile, rows, lag) },
		OnDeferred: func(profile, key string, permanent bool, err error) {
			slog.Error("an object was not indexed", "profile", profile, "object", key, "permanent", permanent, "error", err)
			counts.Deferred(profile, permanent)
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	// Ready when the index database answers and the archive's catalogues can be
	// read: those are the two things an indexer's every object needs.
	mux.Handle("/readyz", readiness.Ready(slog.Default(),
		readiness.Check{Name: "database", Fn: pool.Ping},
		readiness.Check{Name: "catalogues", Fn: func(ctx context.Context) error {
			_, err := archive.List(ctx, store.CataloguePrefix, "", 1)
			return err
		}}))
	server := &http.Server{Addr: cfg.Listen.Address, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("the health listener stopped", "error", err)
		}
	}()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()

	slog.Info("following the archive", "settle", cfg.Settle.D(), "interval", cfg.Interval.D(), "profiles", cfg.Profiles)
	return indexer.Run(ctx)
}

// wakeFrom starts whatever shortens the poll. It returns what releases it.
func wakeFrom(ctx context.Context, w *config.Wake, wake chan<- struct{}) (func(), error) {
	switch {
	case w == nil:
		return func() {}, nil
	case w.NATS != nil:
		stop, err := observe.NATSWake(w.NATS.NATS.URL, w.NATS.NATS.TokenFile, w.NATS.Subject, wake, slog.Default())
		if err != nil {
			return nil, err
		}
		slog.Info("woken by notifications", "subject", w.NATS.Subject)
		return stop, nil
	default: // sqs; the schema admits no other
		var opts []func(*awsconfig.LoadOptions) error
		if w.SQS.Region != "" {
			opts = append(opts, awsconfig.WithRegion(w.SQS.Region))
		}
		cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
		if err != nil {
			return nil, fmt.Errorf("loading the AWS configuration for SQS: %w", err)
		}
		running, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			observe.SQSWake(running, sqs.NewFromConfig(cfg), w.SQS.QueueURL, wake, slog.Default())
		}()
		slog.Info("woken by notifications", "queue", w.SQS.QueueURL)
		return func() { cancel(); <-done }, nil
	}
}
