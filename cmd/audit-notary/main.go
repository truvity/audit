// Command audit-notary seals the archive.
//
// For each profile and tenant it writes, for every hour that has closed and
// settled, one signed seal of what that hour holds, chained to the one before:
// seals/<profile>/<tenant>/<yyyy>/<mm>/<dd>/<hh>.jws
// (docs/reference/bucket-contract.md, docs/decisions/0019-seals.md). It runs
// once and exits, hourly, as a scheduled job.
//
// It is a part of its own (docs/decisions/0016): it reads the records, puts
// seals and signs, and it holds nothing the writer does. The key is a managed
// one the writer's identity cannot use.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"go.opentelemetry.io/otel"

	"github.com/truvity/audit/internal/buildinfo"
	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/internal/config"
	"github.com/truvity/audit/internal/telemetry"
	"github.com/truvity/audit/sdk/catalogue"
	"github.com/truvity/audit/sdk/record"
)

func main() {
	if err := run(); err != nil {
		slog.Error("audit-notary", "error", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "", "the configuration file, or AUDIT_CONFIG: the one thing that configures this process")
	asJSON := flag.Bool("json", false, "print the report as JSON")
	showVersion := flag.Bool("version", false, "print this build's version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("audit-notary", buildinfo.Version)
		return nil
	}
	configFile, err := config.Path(*configPath, "audit-notary")
	if err != nil {
		return err
	}
	cfg, err := config.LoadNotary(configFile)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Metrics are pushed over OTLP when a collector is named, and flushed when
	// the run ends: a job that exits cannot be scraped.
	stopTelemetry, err := telemetry.Start(ctx, "audit-notary", buildinfo.Version, slog.Default())
	if err != nil {
		return err
	}
	defer stopTelemetry(context.Background()) //nolint:errcheck // shutting down
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
		Metrics: metrics, Version: buildinfo.Version, Instance: record.InstanceName(), JSON: *asJSON,
	}
	if cfg.Sink != nil {
		if notary.Catalogue, err = catalogue.Common(); err != nil {
			return err
		}
		if notary.Sink, err = cli.SinkFrom(*cfg.Sink, cfg.Require); err != nil {
			return err
		}
	}
	_, err = notary.Run(ctx)
	return err
}
