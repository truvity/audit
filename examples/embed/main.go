// Command embed is an example application that carries its own audit trail: it
// embeds the writer, so its records go straight into the locked bucket with no
// writer or stream to run beside it, and it serves the query API behind its own
// sign-in, so its console can read the trail with the standard viewer.
//
// It is what docs/guides/embed.md walks through, and it imports only the public
// packages — a test holds it to that — so it is exactly what a module outside
// this repository can do.
//
//	AUDIT_BUCKET=example-audit \
//	AUDIT_KEY_ROOT=/etc/audit/keys/root AUDIT_KEY_DIR=/var/lib/audit/keys \
//	go run ./examples/embed
package main

import (
	"context"
	"embed"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"

	"github.com/truvity/audit/auth"
	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/emit"
	"github.com/truvity/audit/index/s3scan"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/query"
	"github.com/truvity/audit/store"
	"github.com/truvity/audit/store/s3store"
	"github.com/truvity/audit/writer"
)

// The catalogue and the profiles ship with the code. The deployment document
// is the one the chart renders for a standalone writer; an embedding
// application declares its profiles the same way.
//
//go:embed catalogue deployment.yaml
var files embed.FS

func main() {
	if err := run(); err != nil {
		slog.Error("embed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return err
	}
	archive, err := s3store.FromConfig(cfg, s3store.Options{Bucket: os.Getenv("AUDIT_BUCKET")})
	if err != nil {
		return err
	}
	root, err := os.ReadFile(os.Getenv("AUDIT_KEY_ROOT"))
	if err != nil {
		return err
	}
	provider, err := keys.NewLocal(root, os.Getenv("AUDIT_KEY_DIR"))
	if err != nil {
		return err
	}
	defer provider.Close() //nolint:errcheck // shutting down

	app, err := Build(ctx, archive, provider)
	if err != nil {
		return err
	}
	defer app.Close(context.Background()) //nolint:errcheck // shutting down

	server := &http.Server{Addr: ":8080", Handler: app.Handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// App is the application's audit trail: where it records, and where its
// console reads.
type App struct {
	Writer  *writer.Writer
	Emitter *emit.Emitter
	Query   *query.Service
	Handler http.Handler
}

// Build wires the trail: a writer on the archive, the application's emitter
// writing into it in process, and the query API reading the archive back.
func Build(ctx context.Context, archive store.Store, provider keys.Provider) (*App, error) {
	doc, err := files.ReadFile("deployment.yaml")
	if err != nil {
		return nil, err
	}
	deployment, err := preset.ParseDeployment(doc)
	if err != nil {
		return nil, err
	}
	presets, err := preset.Builtin()
	if err != nil {
		return nil, err
	}
	profiles, err := deployment.Compose(presets)
	if err != nil {
		return nil, err
	}
	shop, err := catalogue.LoadFS(files, "catalogue/shop.yaml")
	if err != nil {
		return nil, err
	}

	// The writer. No database here, so it indexes nothing and runs as one
	// instance; give it a *pgxpool.Pool for an index and several replicas.
	w, err := writer.Open(ctx, writer.Config{
		Archive:    archive,
		Profiles:   profiles,
		Keys:       provider,
		Catalogues: []*catalogue.Catalogue{shop},
		Self:       "workload:shop",
	})
	if err != nil {
		return nil, err
	}

	// The application's emitter writes into the writer in process: a record
	// declared `block` is in the archive before Record returns.
	emitter, err := emit.New(emit.Options{Source: shop.Source, Catalogue: shop, Sink: w})
	if err != nil {
		_ = w.Close(ctx)
		return nil, err
	}

	// The query API, behind the application's own sign-in. Here the operator
	// is whoever the gateway in front says it signed in — the application's
	// own session lookup goes in its place. Reads are recorded into the same
	// writer.
	var sealer keys.Sealer
	if s, ok := provider.(keys.Sealer); ok {
		sealer = s
	}
	q, err := query.New(query.Config{
		Searcher:      &s3scan.Scanner{Store: archive},
		Authenticator: auth.AuthenticatorFunc(signedIn),
		Authorizer: auth.Declarative{Rules: []auth.Rule{{
			Name: "operators", Claim: "role", Value: "operator",
			Grant: auth.Grant{
				AllTenants: true, Profiles: []string{"security", "history"},
				Operations: []auth.Operation{auth.Search, auth.Facets, auth.Get},
			},
		}}},
		Sink:    w,
		Archive: archive,
		Keys:    sealer,
	})
	if err != nil {
		_ = emitter.Close()
		_ = w.Close(ctx)
		return nil, err
	}

	path, handler := q.Handler()
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	return &App{Writer: w, Emitter: emitter, Query: q, Handler: mux}, nil
}

// signedIn is where the application's own session lookup goes. This example
// trusts the gateway in front of it, which verified the user and says so in a
// header the gateway strips from anything a client sent.
func signedIn(_ context.Context, r *http.Request) (auth.Principal, error) {
	user := r.Header.Get("X-Signed-In-User")
	if user == "" {
		return auth.Principal{}, errors.New("not signed in")
	}
	return auth.Principal{
		Issuer: "shop", Subject: user, Via: "shop-session",
		Claims: map[string][]string{"role": r.Header.Values("X-Signed-In-Role")},
	}, nil
}

// Close stops the query service, then the emitter, then the writer, so that
// every record in flight — the reads included — is written before it goes.
func (a *App) Close(ctx context.Context) error {
	return errors.Join(a.Query.Close(), a.Emitter.Close(), a.Writer.Close(ctx))
}
