// Command audit-writer-lambda is the write path as an AWS Lambda function behind
// an SQS event source mapping.
//
// It is the same writer as audit-writer's: the package writer, opened over the
// same archive with the same profiles under the same `require: archived` guard,
// and a record is acknowledged only once it is in the bucket. What differs is
// everything around it, and all of it follows from there being no process that
// stays up:
//
//   - the input is the SQS event, with partial batch responses
//     (ReportBatchItemFailures), so a message that failed goes back to the queue
//     and the ones that did not are deleted by the platform;
//   - there is no database, so the deduplication table is DynamoDB
//     (dedupe/dynamodbdedupe), shared by every concurrent invocation environment;
//   - there is no listener, no registry, no stream: the catalogues the writer
//     runs with are files in the package;
//   - configuration is a file in the package (docs/decisions/0021): the Pulumi
//     library renders it from the stack's own outputs and ships it in the zip, so
//     there is nothing to fetch at cold start and nothing in the environment
//     that is a secret;
//   - telemetry is OTEL_* and nothing else, sent to the extension's loopback
//     proxy (docs/deployment/aws.md), and flushed at the end of every
//     invocation because an environment is frozen between them, and again on
//     SIGTERM.
//
// This is not deployed by this repository; deploy/pulumi is the library that
// builds it into a stack.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"go.opentelemetry.io/otel"

	"github.com/truvity/audit/dedupe/dynamodbdedupe"
	"github.com/truvity/audit/internal/buildinfo"
	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/internal/config"
	"github.com/truvity/audit/internal/telemetry"
	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/sdk/catalogue"
	"github.com/truvity/audit/sdk/sink"
	"github.com/truvity/audit/writer"
)

// defaultConfig is where the Pulumi library puts the configuration file in the
// function's package: /var/task is the package's root.
const defaultConfig = "/var/task/audit.yaml"

func main() {
	if err := run(); err != nil {
		slog.Error("audit-writer-lambda", "error", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", defaultConfig, "the configuration file: the one thing that configures this process")
	showVersion := flag.Bool("version", false, "print this build's version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("audit-writer-lambda", buildinfo.Version)
		return nil
	}
	cfg, err := config.LoadWriterLambda(*configPath)
	if err != nil {
		return err
	}

	// SIGTERM is the platform's shutdown, sent when an extension is registered
	// (the telemetry layer is one) and the environment is idle: the one chance to
	// write what is held and export what is buffered.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	presets, err := preset.Builtin()
	if err != nil {
		return err
	}
	d, err := cli.LoadDeployment(cfg.Deployment)
	if err != nil {
		return err
	}
	profiles, err := d.Compose(presets)
	if err != nil {
		return err
	}
	// A profile whose frameworks demand a lock this bucket is not written with
	// is refused before a copy lands where it could be deleted (ADR 0014).
	if err := preset.CheckLockMode(profiles, cfg.Archive.LockMode); err != nil {
		return err
	}
	archive, err := cli.OpenArchiveFrom(ctx, cfg.Archive)
	if err != nil {
		return err
	}
	provider, err := cli.OpenKeysFrom(ctx, cfg.Keys)
	if err != nil {
		return err
	}
	if provider != nil {
		defer provider.Close() //nolint:errcheck // shutting down
	}
	var found []*catalogue.Catalogue
	if cfg.Catalogues != "" {
		if found, err = loadCatalogues(cfg.Catalogues); err != nil {
			return err
		}
	}

	table := cfg.Dedupe.DynamoDB
	var opts []func(*awsconfig.LoadOptions) error
	if table.Region != "" {
		opts = append(opts, awsconfig.WithRegion(table.Region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return fmt.Errorf("loading the AWS configuration for DynamoDB: %w", err)
	}
	window := table.Window.D()
	if window == 0 {
		window = widestDedupeWindow(profiles)
	}
	seen, err := dynamodbdedupe.New(dynamodb.NewFromConfig(awsCfg), table.Table, window)
	if err != nil {
		return err
	}

	stopTelemetry, err := telemetry.Start(ctx, "audit-writer", buildinfo.Version, slog.Default())
	if err != nil {
		return err
	}
	queue, err := telemetry.NewQueue(otel.GetMeterProvider())
	if err != nil {
		return err
	}

	w, err := writer.Open(ctx, writer.Config{
		Archive:          archive,
		Profiles:         profiles,
		Keys:             provider,
		Catalogues:       found,
		Dedupe:           seen,
		ForgetIdentities: cfg.ForgetIdentities,
		Version:          buildinfo.Version,
		// Concurrent invocations are replicas: each environment is its own
		// writer sharing one deduplication table. Two is "more than one" to the
		// guards that care.
		Replicas: 2,
		// What is on the queue was stamped by a receiver of this installation,
		// which is the only principal the queue's policy lets send; there is no
		// caller here to verify, and re-stamping would replace the identity the
		// receiver checked with nothing.
		FromStream: true,
	})
	if err != nil {
		return err
	}
	front, err := guard(w, cfg.Require)
	if err != nil {
		return err
	}

	h := &handler{
		target: front, age: queue, now: time.Now, log: slog.Default(),
		before: func(ctx context.Context) {
			if err := w.RefreshHolds(ctx); err != nil {
				// The last answer stands, as it does for the background refresh.
				slog.Error("could not refresh the legal holds; keeping the last answer", "error", err)
			}
		},
		after: func(ctx context.Context) {
			flush, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			if err := telemetry.Flush(flush); err != nil {
				slog.Warn("could not export telemetry at the end of the invocation", "error", err)
			}
		},
	}

	go func() {
		<-ctx.Done()
		shutting, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := w.Close(shutting); err != nil {
			slog.Error("flushing on shutdown", "error", err)
		}
		_ = stopTelemetry(shutting)
		// The runtime library has no way to be told to return, and the platform
		// is about to end the environment either way.
		os.Exit(0)
	}()

	slog.Info("audit-writer-lambda", "bucket", cfg.Archive.Bucket.Name, "profiles", len(profiles),
		"table", table.Table, "window", window.String(), "lock", cfg.Archive.LockMode)
	lambda.StartWithOptions(h.Handle, lambda.WithContext(ctx))
	return nil
}

// guard holds the writer to `require`: it refuses at start-up when the chain can
// never give it, and fails any write acknowledged below it.
func guard(front sink.Sink, require string) (sink.Sink, error) {
	least, err := sink.ParseDurability(require)
	if err != nil {
		return nil, fmt.Errorf("require: %w", err)
	}
	guarded, err := sink.Guard(front, least)
	if err != nil {
		return nil, fmt.Errorf("refusing to start with require: %s: %w", require, err)
	}
	return guarded, nil
}

// widestDedupeWindow is how long a written identifier is remembered when the
// file does not say: the widest window any profile's presets ask for, since one
// table serves them all.
func widestDedupeWindow(profiles map[string]*preset.Profile) time.Duration {
	var longest time.Duration
	for _, p := range profiles {
		if d := time.Duration(p.Pipeline.DedupeWindowDays) * 24 * time.Hour; d > longest {
			longest = d
		}
	}
	return longest
}

// loadCatalogues reads every catalogue in a directory.
func loadCatalogues(dir string) ([]*catalogue.Catalogue, error) {
	paths, err := cli.FindCatalogues(dir)
	if err != nil {
		return nil, err
	}
	out := make([]*catalogue.Catalogue, 0, len(paths))
	for _, path := range paths {
		c, err := catalogue.LoadFS(os.DirFS(filepath.Dir(path)), filepath.Base(path))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, errors.New("catalogues names " + dir + ", which holds no catalogue")
	}
	return out, nil
}
