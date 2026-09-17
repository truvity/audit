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

	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/internal/writer"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
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
		listen     = flag.String("listen", env("AUDIT_LISTEN", ":8080"), "address to serve the sink on")
		rollEvery  = flag.Duration("roll-interval", 5*time.Minute, "how long an object stays open")
		version    = flag.String("version", env("AUDIT_VERSION", "dev"), "this build's version")
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

	registry := &writer.Registry{}
	if *catalogues != "" {
		if err := registerAll(registry, *catalogues); err != nil {
			return err
		}
	}

	dedupe := writer.Dedupe(&writer.MemoryDedupe{})
	if err := writer.GuardReplicas(*replicas, dedupe); err != nil {
		return err
	}

	longest := longestRetention(profiles)
	w, err := writer.New(&writer.Writer{
		Catalogues: registry,
		Splitter:   &writer.Splitter{Profiles: profiles, Keys: provider},
		Roller: &writer.Roller{
			Store: archive, Instance: record.InstanceName(), Interval: *rollEvery,
			OnPut: func(key string, records int) {
				slog.Info("object written", "key", key, "records", records)
			},
		},
		Dedupe: dedupe,
		DeadLetter: &writer.StoreDeadLetter{
			Store: archive, Instance: record.InstanceName(),
			RetainUntil: func(at time.Time) time.Time { return at.Add(longest) },
		},
		Archive: &writer.SchemaArchive{
			Store: archive,
			// A record whose schema has been deleted is a record nobody can
			// read, so what describes records outlives the longest of them.
			RetainUntil: func(at time.Time) time.Time { return at.Add(longest) },
		},
		Version: *version,
		Hooks: writer.Hooks{
			OnDeadLettered: func(r *record.Record, reason string) {
				slog.Error("dead letter", "id", r.GetId(), "action", r.GetAction(), "reason", reason)
			},
			OnUnhandled: func(action string, p []string) {
				slog.Warn("no configured profile keeps these", "action", action, "profiles", p)
			},
		},
	})
	if err != nil {
		return err
	}
	defer w.Close(context.Background()) //nolint:errcheck // shutting down

	path, handler := sink.NewHandler(w)
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

func registerAll(r *writer.Registry, dir string) error {
	found, err := cli.FindCatalogues(dir)
	if err != nil {
		return err
	}
	for _, path := range found {
		c, err := catalogue.LoadFS(os.DirFS(dirOf(path)), baseOf(path))
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		r.Register(c)
		slog.Info("catalogue registered", "source", c.Source, "version", c.Version)
	}
	return nil
}

// longestRetention is how long the longest-lived profile keeps a copy, which is
// what anything that has to outlive every record is kept for.
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
