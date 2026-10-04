// Command audit-notary-lambda is the notary as an AWS Lambda function invoked
// hourly by an EventBridge Scheduler schedule.
//
// It is audit-notary's own logic (internal/cli.Notary): for each profile and
// tenant, every hour that has closed and settled and has no seal gets one, signed
// ES384 by a KMS key and chained to the one before (ADR 0019). A run is
// idempotent, so the schedule's retry, an overlapping invocation and a missed
// hour are all harmless: the next run seals what is missing, and an hour that is
// already sealed is read and left alone.
//
// What differs from the Job is the shell around it. The configuration is the
// same file (schemas/config/audit-notary.schema.json), in the package at
// /var/task/audit.yaml, with `signer.kms` naming the seal key; the credentials
// are the function role's, which is not the writer's; the report is a log line
// and not standard output; a run that could not seal a tenant FAILS the
// invocation, which is what the Errors alarm is on; and telemetry is flushed
// before the invocation returns, because the environment is frozen after it.
//
// The notary does not record `audit.seal.written` through a writer here unless
// the file's `sink` names one this function can reach: a function outside a VPC
// cannot reach an in-cluster writer, so the seals themselves, which are in the
// bucket and verifiable, are the record.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"go.opentelemetry.io/otel"

	"github.com/truvity/audit/internal/buildinfo"
	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/internal/config"
	"github.com/truvity/audit/internal/telemetry"
	"github.com/truvity/audit/sdk/catalogue"
	"github.com/truvity/audit/sdk/record"
)

// The configuration file is where the layer of the Pulumi library mounts it,
// /opt/audit/audit.yaml, unless --config or AUDIT_CONFIG names another. The old
// place, the function package's own root, is still tried when the layer's is
// absent, so that a deployment built by the previous release keeps starting;
// that fallback goes in the release after this one.
const (
	defaultConfig = "/opt/audit/audit.yaml"
	legacyConfig  = "/var/task/audit.yaml"
)

func main() {
	if err := run(); err != nil {
		slog.Error("audit-notary-lambda", "error", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "", "the configuration file, or AUDIT_CONFIG, or "+defaultConfig+": the one thing that configures this process")
	showVersion := flag.Bool("version", false, "print this build's version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("audit-notary-lambda", buildinfo.Version)
		return nil
	}
	configFile, err := config.Path(*configPath, "audit-notary", defaultConfig, legacyConfig)
	if err != nil {
		return err
	}
	cfg, err := config.LoadNotary(configFile)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	stopTelemetry, err := telemetry.Start(ctx, "audit-notary", buildinfo.Version, slog.Default())
	if err != nil {
		return err
	}
	metrics, err := telemetry.NewNotary(otel.GetMeterProvider())
	if err != nil {
		return err
	}
	archive, err := cli.OpenArchiveFrom(ctx, cfg.Archive)
	if err != nil {
		return err
	}
	signer, err := cli.OpenSignerFrom(ctx, cfg.Signer)
	if err != nil {
		return err
	}
	notary := cli.Notary{
		Store: archive, Signer: signer, Profiles: cfg.Profiles, Settle: cfg.Settle.D(),
		Metrics: metrics, Version: buildinfo.Version, Instance: record.InstanceName(),
		// The report is logged below; a Lambda's standard output is its log.
		Out: io.Discard,
	}
	if cfg.Sink != nil {
		if notary.Catalogue, err = catalogue.Common(); err != nil {
			return err
		}
		if notary.Sink, err = cli.SinkFrom(*cfg.Sink, cfg.Require); err != nil {
			return err
		}
	}

	h := func(ctx context.Context, _ json.RawMessage) (*cli.NotaryReport, error) {
		report, err := notary.Run(ctx)
		if report != nil {
			slog.InfoContext(ctx, "audit-notary-lambda", "sealed", len(report.Sealed), "present", report.Present,
				"failed", len(report.Failures), "key", report.Key)
			for _, f := range report.Failures {
				slog.ErrorContext(ctx, "a tenant could not be sealed further",
					"profile", f.Profile, "tenant", f.Tenant, "reason", f.Reason)
			}
		}
		// Frozen after the invocation: export now, or the series is a run late.
		flush, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if ferr := telemetry.Flush(flush); ferr != nil {
			slog.Warn("could not export telemetry at the end of the invocation", "error", ferr)
		}
		return report, err
	}

	go func() {
		<-ctx.Done()
		shutting, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = stopTelemetry(shutting)
		os.Exit(0)
	}()
	slog.Info("audit-notary-lambda", "bucket", cfg.Archive.Bucket.Name, "settle", cfg.Settle.D().String())
	lambda.StartWithOptions(h, lambda.WithContext(ctx))
	return errors.New("the Lambda runtime returned")
}
