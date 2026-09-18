// Command audit-registry keeps the catalogues a deployment has registered.
//
// A catalogue is authored next to the code that emits against it and registered
// here at deploy time. That is what keeps it true: a catalogue kept centrally
// and edited by hand drifts from the code within a release or two, and then the
// thing that describes the records is wrong about them.
//
// Registration is where the deployment gets its say — the document is held to
// the same toolchain that validates it in the application's own tests, the
// source is checked against who is calling, and the categories the deployment's
// profiles require are checked against what the catalogue carries.
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
	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/index/postgres"
	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/internal/registry"
	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

func main() {
	if err := run(); err != nil {
		slog.Error("audit-registry", "error", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		database   = flag.String("database", env("AUDIT_DATABASE", ""), "the Postgres URL the catalogues are kept in")
		deployment = flag.String("deployment", env("AUDIT_DEPLOYMENT", ""), "the profile configuration")
		listen     = flag.String("listen", env("AUDIT_LISTEN", ":8080"), "address to serve on")
		sinkURL    = flag.String("sink", env("AUDIT_SINK", ""), "the writer registrations are recorded through")
		workloads  = flag.String("workloads", env("AUDIT_WORKLOADS", ""),
			"the file naming the trusted issuers and the source each workload speaks for")
		version = flag.String("version", env("AUDIT_VERSION", "dev"), "this build's version")
	)
	flag.Parse()

	switch {
	case *database == "":
		return errors.New("give the Postgres URL with --database")
	case *workloads == "":
		return errors.New(
			"give --workloads: the registry decides whose catalogue a document is from the " +
				"caller's verified identity, and without a way to verify one it would register nothing")
	case *deployment == "":
		return errors.New(
			"give the profile configuration with --deployment: without it nothing checks that a " +
				"catalogue carries the categories this deployment's profiles require")
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

	callers, err := cli.LoadWorkloads(*workloads)
	if err != nil {
		return err
	}
	authenticator, err := auth.NewJWT(ctx, callers.Issuers, slog.Default())
	if err != nil {
		return err
	}

	pool, err := pgxpool.New(ctx, *database)
	if err != nil {
		return err
	}
	defer pool.Close()
	// Like every other service here, it refuses to start on a schema version it
	// does not know rather than migrating itself.
	if err := postgres.CheckVersion(ctx, pool); err != nil {
		return err
	}

	common, err := catalogue.Common()
	if err != nil {
		return err
	}
	r := &registry.Registry{
		Store:    registry.Postgres{DB: pool},
		Profiles: profiles,
		Builtin:  []*catalogue.Catalogue{common},
		// A gap in coverage is the deployment's to close, not a reason to
		// refuse the application that registered while it was open.
		OnUncovered: func(_ context.Context, profile string, missing []string) {
			slog.Warn("a profile requires categories no registered catalogue emits",
				"profile", profile, "missing", missing)
		},
		// Whose catalogue a document is comes from the caller's verified
		// service account, never from the document and never from a header.
		Identity: callers.Map.SourceFrom,
	}
	if *sinkURL != "" {
		r.OnRegistered = recorder(cli.WriterClient(*sinkURL), *version)
	} else {
		slog.Warn("no writer configured: registrations will not be recorded")
	}

	path, handler := registry.NewHandler(r)
	mux := http.NewServeMux()
	mux.Handle(path, auth.Middleware(authenticator, handler))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	server := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()

	slog.Info("serving the registry", "listen", *listen, "profiles", len(profiles))
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
			Records: []*record.Record{{
				Action:           "audit.catalogue.registered",
				Operation:        auditv1.Operation_OPERATION_CREATE,
				TenantId:         record.TenantPlatform,
				Source:           "audit",
				CatalogueVersion: catalogueVersion(),
				SchemaVersion:    record.SchemaVersion,
				Id:               record.NewID(),
				Actor:            &record.Actor{Kind: "service", Id: e.RegisteredBy},
				Observer:         &record.Observer{Version: version, Instance: record.InstanceName()},
				Outcome:          &record.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS},
				Targets: []*record.Target{
					{Type: "catalogue", Id: e.Source + "@" + e.Version},
				},
			}},
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

func catalogueVersion() string {
	c, err := catalogue.Common()
	if err != nil {
		return ""
	}
	return c.Version
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
