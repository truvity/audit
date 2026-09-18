// Command audit-query answers questions about what was written.
//
// It reads; it never writes to the archive. The one thing it does write is the
// record of each read, through the writer like anything else: a trail that
// shows what everyone did except who looked at it is missing the half an
// investigation usually starts from.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/truvity/audit/auth"
	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/index"
	"github.com/truvity/audit/index/postgres"
	"github.com/truvity/audit/index/s3scan"
	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/internal/query"
	"github.com/truvity/audit/sink"
)

func main() {
	if err := run(); err != nil {
		slog.Error("audit-query", "error", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		searcher = flag.String("searcher", env("AUDIT_SEARCHER", "postgres"),
			"where answers come from: postgres, or s3scan for a deployment with no database")
		database = flag.String("database", env("AUDIT_DATABASE", ""), "the Postgres URL of the index")
		bucket   = flag.String("bucket", env("AUDIT_BUCKET", ""), "the bucket the archive is in")
		prefix   = flag.String("prefix", env("AUDIT_PREFIX", ""), "the prefix within the bucket")
		region   = flag.String("region", env("AUDIT_REGION", ""), "the region, when it is not in the environment")
		grants   = flag.String("grants", env("AUDIT_GRANTS", ""),
			"the file mapping claims to grants; without it nobody is granted anything")
		sinkURL = flag.String("sink", env("AUDIT_SINK", ""),
			"the writer reads are recorded through")
		listen  = flag.String("listen", env("AUDIT_LISTEN", ":8080"), "address to serve on")
		version = flag.String("version", env("AUDIT_VERSION", "dev"), "this build's version")
	)
	flag.Parse()

	if *sinkURL == "" {
		return errors.New(
			"give the writer with --sink: reading an audit trail is itself an auditable event, " +
				"and a query service that records none is half a service")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	rules, err := cli.LoadGrants(*grants)
	if err != nil {
		return err
	}
	found, err := searcherFor(ctx, *searcher, *database, *bucket, *prefix, *region)
	if err != nil {
		return err
	}
	common, err := catalogue.Common()
	if err != nil {
		return err
	}

	service, err := query.New(&query.Service{
		Searcher:   found,
		Authorizer: rules,
		Sink:       sink.NewClient(nil, *sinkURL),
		Catalogue:  common,
		Version:    *version,
		OnUnrecorded: func(action string, err error) {
			// Not a degraded service: this is the service failing at one of
			// the two things it is for.
			slog.Error("a read was not recorded", "action", action, "error", err)
		},
	})
	if err != nil {
		return err
	}
	defer service.Close() //nolint:errcheck // shutting down

	// Until an authenticator lands, the deployment puts one in front and this
	// refuses to invent an identity: `none` gives every caller the same empty
	// principal, and the declarative authorizer denies a principal with no
	// subject.
	path, handler := query.NewHandler(service, auth.None{})
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	server := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()

	slog.Info("serving queries", "listen", *listen, "searcher", *searcher)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// searcherFor builds the searcher a deployment asked for.
func searcherFor(ctx context.Context, kind, database, bucket, prefix, region string) (index.Searcher, error) {
	switch kind {
	case "postgres":
		if database == "" {
			return nil, errors.New("the postgres searcher needs --database")
		}
		pool, err := pgxpool.New(ctx, database)
		if err != nil {
			return nil, err
		}
		if err := postgres.CheckVersion(ctx, pool); err != nil {
			return nil, err
		}
		return postgres.New(pool)
	case "s3scan":
		if bucket == "" {
			return nil, errors.New("the s3scan searcher needs --bucket")
		}
		archive, err := cli.Archive(ctx, bucket, prefix, region)
		if err != nil {
			return nil, err
		}
		return &s3scan.Scanner{Store: archive}, nil
	default:
		return nil, errors.New("--searcher is postgres or s3scan")
	}
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
