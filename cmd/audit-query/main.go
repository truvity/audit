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
	"github.com/truvity/audit/index"
	"github.com/truvity/audit/index/postgres"
	"github.com/truvity/audit/index/s3scan"
	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/query"
	"github.com/truvity/audit/store"
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
			"the file naming the trusted issuers and mapping their claims to grants")
		deployment = flag.String("deployment", env("AUDIT_DEPLOYMENT", ""),
			"the profile configuration, which a grant preset turns roles into profiles with")
		sinkURL = flag.String("sink", env("AUDIT_SINK", ""),
			"the writer reads are recorded through")
		exports = flag.String("exports", env("AUDIT_EXPORTS", ""),
			"the bucket exports are written to; without it the export operation is refused")
		exportExpiry = flag.Duration("export-expiry", 7*24*time.Hour,
			"how long an export is kept before the bucket clears it")
		linkValid = flag.Duration("export-link-valid", time.Hour,
			"how long a download link works")
		listen  = flag.String("listen", env("AUDIT_LISTEN", ":8080"), "address to serve on")
		version = flag.String("version", env("AUDIT_VERSION", "dev"), "this build's version")
	)
	keyFlags := cli.NewKeyFlags(flag.CommandLine, env)
	flag.Parse()

	if *sinkURL == "" {
		return errors.New(
			"give the writer with --sink: reading an audit trail is itself an auditable event, " +
				"and a query service that records none is half a service")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var profiles map[string]*preset.Profile
	if *deployment != "" {
		presets, err := preset.Builtin()
		if err != nil {
			return err
		}
		d, err := cli.LoadDeployment(*deployment)
		if err != nil {
			return err
		}
		if profiles, err = d.Compose(presets); err != nil {
			return err
		}
	}
	access, err := cli.LoadAccess(*grants, profiles)
	if err != nil {
		return err
	}
	if len(access.Issuers) == 0 {
		return errors.New(
			"the grants file names no issuers, so nobody could ever authenticate: " +
				"a query service nobody can use is a misconfiguration, not a safe default")
	}
	authenticator, err := auth.NewJWT(ctx, access.Issuers, slog.Default())
	if err != nil {
		return err
	}
	found, err := searcherFor(ctx, *searcher, *database, *bucket, *prefix, *region)
	if err != nil {
		return err
	}
	if *exports != "" && *exports == *bucket {
		return errors.New(
			"--exports must not be the archive bucket: an export is an unlocked copy meant to be " +
				"cleared, and the archive's policy denies every delete, so it would stay forever")
	}
	exportTo, err := exportsFor(ctx, *exports, *region, *exportExpiry, *linkValid)
	if err != nil {
		return err
	}

	// The archive, when named, is where a record's standing in the digest
	// chain is read for Get. Without it Get still answers, with where the copy
	// is and nothing about whether it has been verified.
	var archive store.Store
	if *bucket != "" {
		if archive, err = cli.Archive(ctx, *bucket, *prefix, *region); err != nil {
			return err
		}
	}

	// Resolve is offered only when this service is given the keys and the
	// archive. It is a separate privilege from reading: a deployment that
	// does not mount the keys here has a query service that cannot undo a
	// pseudonym at all, whatever a grant says.
	var sealer keys.Sealer
	if keyFlags.Configured() {
		if archive == nil || (keyFlags.Local() && *keyFlags.Dir == "") {
			return errors.New("resolve needs the writer's keys (--key-root and --key-dir, " +
				"or --key-provider transit) and --bucket together")
		}
		provider, err := keyFlags.Open(ctx)
		if err != nil {
			return err
		}
		if provider == nil {
			return errors.New(
				"resolve is enabled and no key provider is configured: there is nothing to open, " +
					"because nothing was sealed. Turn resolve off, or name a provider")
		}
		defer provider.Close() //nolint:errcheck // shutting down
		var ok bool
		if sealer, ok = provider.(keys.Sealer); !ok {
			return errors.New("this key provider cannot open what the writer sealed, so resolve is impossible")
		}
	}

	service, err := query.New(query.Config{
		Searcher:      found,
		Authenticator: authenticator,
		Authorizer:    access.Rules,
		Sink:          cli.WriterClient(*sinkURL),
		Archive:       archive,
		Keys:          sealer,
		Exports:       exportTo,
		Version:       *version,
	})
	if err != nil {
		return err
	}
	defer service.Close() //nolint:errcheck // shutting down

	path, handler := service.Handler()
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

// exportsFor prepares exports, when a deployment has somewhere to put them.
//
// The bucket is its own, not the archive's, and has no Object Lock. An export
// is a copy of records made to be taken away and then cleared; the archive's
// bucket policy denies every delete, so an export written there would stay
// forever, and the bucket needs a lifecycle rule on the export prefix, which
// is a rule nobody should ever write against the archive.
func exportsFor(
	ctx context.Context, bucket, region string, expiry, linkValid time.Duration,
) (*query.Exports, error) {
	if bucket == "" {
		return nil, nil
	}
	files, err := cli.ExportStore(ctx, bucket, region)
	if err != nil {
		return nil, err
	}
	presigner, ok := files.(store.Presigner)
	if !ok {
		return nil, errors.New("the export bucket cannot sign links")
	}
	return &query.Exports{
		Store: files, Presigner: presigner,
		Expiry: expiry, LinkValid: linkValid,
	}, nil
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
		return postgres.NewReader(pool)
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
