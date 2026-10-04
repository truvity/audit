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
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/truvity/audit/authn"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/truvity/audit/index"
	"github.com/truvity/audit/index/postgres"
	"github.com/truvity/audit/index/s3scan"
	"github.com/truvity/audit/internal/buildinfo"
	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/internal/config"
	"github.com/truvity/audit/internal/telemetry"
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
	configPath := flag.String("config", "", "the configuration file, or AUDIT_CONFIG: the one thing that configures this process")
	showVersion := flag.Bool("version", false, "print this build's version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("audit-query", buildinfo.Version)
		return nil
	}
	configFile, err := config.Path(*configPath, "audit-query")
	if err != nil {
		return err
	}
	cfg, err := config.LoadQuery(configFile)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Metrics and traces, pushed over OTLP when a collector is named in the
	// environment and a no-op otherwise.
	stopTelemetry, err := telemetry.Start(ctx, "audit-query", buildinfo.Version, slog.Default())
	if err != nil {
		return err
	}
	defer stopTelemetry(context.Background()) //nolint:errcheck // shutting down

	var profiles map[string]*preset.Profile
	if cfg.Deployment != "" {
		presets, err := preset.Builtin()
		if err != nil {
			return err
		}
		d, err := cli.LoadDeployment(cfg.Deployment)
		if err != nil {
			return err
		}
		if profiles, err = d.Compose(presets); err != nil {
			return err
		}
	}
	access, err := cli.LoadAccess(cfg.Grants, profiles)
	if err != nil {
		return err
	}
	if len(access.Issuers) == 0 {
		return errors.New(
			"the grants file names no issuers, so nobody could ever authenticate: " +
				"a query service nobody can use is a misconfiguration, not a safe default")
	}
	authenticator, err := authn.NewJWT(ctx, access.Issuers, slog.Default())
	if err != nil {
		return err
	}
	found, err := searcherFor(ctx, cfg)
	if err != nil {
		return err
	}
	exportTo, err := exportsFor(ctx, cfg.Exports)
	if err != nil {
		return err
	}

	// The archive, when named, is where the sealed identities are read for
	// resolve. Without it resolve is not offered.
	var archive store.Store
	if cfg.Archive != nil {
		if archive, err = cli.OpenArchiveFrom(ctx, *cfg.Archive); err != nil {
			return err
		}
	}

	// Resolve is offered only when this service is given the keys and the
	// archive. It is a separate privilege from reading: a deployment that
	// does not mount the keys here has a query service that cannot undo a
	// pseudonym at all, whatever a grant says.
	var sealer keys.Sealer
	if cfg.Keys.Enabled() {
		provider, err := cli.OpenKeysFrom(ctx, cfg.Keys)
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

	// The writer this service records through, held to `require` before the
	// service opens: one that cannot give it is a start-up error.
	recordTo, err := cli.SinkFrom(cfg.Sink, cfg.Require)
	if err != nil {
		return err
	}
	service, err := query.New(query.Config{
		Searcher:      found,
		Authenticator: authenticator,
		Authorizer:    access.Rules,
		Sink:          recordTo,
		Archive:       archive,
		Keys:          sealer,
		Exports:       exportTo,
		Version:       buildinfo.Version,
	})
	if err != nil {
		return err
	}
	defer service.Close() //nolint:errcheck // shutting down

	path, handler := service.Handler()
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	server := &http.Server{Addr: cfg.Listen.Address, Handler: telemetry.HTTPHandler(mux, "audit-query"), ReadHeaderTimeout: 10 * time.Second}

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()

	slog.Info("serving queries", "listen", cfg.Listen.Address, "searcher", cfg.Searcher)
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
func exportsFor(ctx context.Context, exports *config.Exports) (*query.Exports, error) {
	if exports == nil {
		return nil, nil
	}
	files, err := cli.OpenExportsFrom(ctx, exports.Bucket)
	if err != nil {
		return nil, err
	}
	return &query.Exports{
		Store: files, Presigner: files,
		Expiry: exports.Expiry.D(), LinkValid: exports.LinkValid.D(),
	}, nil
}

// searcherFor builds the searcher a deployment asked for.
func searcherFor(ctx context.Context, cfg *config.Query) (index.Searcher, error) {
	switch cfg.Searcher {
	case "postgres":
		poolConfig, err := cfg.Database.PoolConfig()
		if err != nil {
			return nil, err
		}
		pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
		if err != nil {
			return nil, err
		}
		if err := postgres.CheckVersion(ctx, pool); err != nil {
			return nil, err
		}
		return postgres.NewReader(pool)
	default: // s3scan; the schema admits no other
		scanned, err := cli.OpenArchiveFrom(ctx, *cfg.Archive)
		if err != nil {
			return nil, err
		}
		return &s3scan.Scanner{Store: scanned}, nil
	}
}
