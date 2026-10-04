package main

// The scheduled jobs take their configuration from a file: `audit <job>
// --config <file>`, validated against schemas/config/audit-<job>.schema.json
// before anything starts. The flags of the same commands stay, for a person at
// a keyboard; a job in a cluster is configured by the file and by nothing else,
// so that a deployment is one reviewable document
// (docs/decisions/0021-one-validated-configuration-file.md).

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/truvity/audit/index/postgres"
	"github.com/truvity/audit/internal/buildinfo"
	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/internal/config"
	"github.com/truvity/audit/sdk/catalogue"
	"github.com/truvity/audit/sdk/record"
)

const configUsage = "the configuration file (or AUDIT_CONFIG, when no other option is given); with it, nothing else configures the command"

// onlyConfig refuses a command line that names a configuration file and also
// configures the command another way: two answers to one question, and the
// file is the one that is reviewed. --json only changes how the report is
// printed, so it may stay.
// jobConfig is the file a job reads: --config when it is given, otherwise
// AUDIT_CONFIG, but only on a command line with no option of its own. A job in a
// cluster is configured by the environment and nothing else; a person at a
// terminal who happens to have AUDIT_CONFIG exported and who gives --deployment
// is configuring the command by hand, and is not told to put that in a file
// they never named.
func jobConfig(flags *flag.FlagSet, flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	extra := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name != "json" {
			extra = true
		}
	})
	if extra {
		return ""
	}
	return strings.TrimSpace(os.Getenv(config.EnvConfig))
}

func onlyConfig(flags *flag.FlagSet) error {
	var extra []string
	flags.Visit(func(f *flag.Flag) {
		if f.Name != "config" && f.Name != "json" {
			extra = append(extra, "--"+f.Name)
		}
	})
	if len(extra) > 0 {
		return fmt.Errorf("with --config nothing else configures the command, and %s was also given: "+
			"put it in the file", strings.Join(extra, ", "))
	}
	return nil
}

func verifyFromConfig(path string, asJSON bool) error {
	cfg, err := config.LoadVerify(path)
	if err != nil {
		return err
	}
	profiles, err := profilesFor(cfg.Deployment)
	if err != nil {
		return err
	}
	names := cfg.Profiles
	if len(names) == 0 {
		for name := range profiles {
			names = append(names, name)
		}
		sort.Strings(names)
	}
	for _, name := range names {
		if _, ok := profiles[name]; !ok {
			return fmt.Errorf("profiles: the deployment has no profile %q", name)
		}
	}
	ctx := context.Background()
	archive, err := cli.OpenArchiveFrom(ctx, cfg.Archive)
	if err != nil {
		return err
	}
	// A scheduled run says "the last day"; the image the jobs run from has no
	// shell, so the arithmetic lives here.
	end := time.Now().UTC().Truncate(time.Hour)
	start := end.Add(-cfg.Last.D())

	var problems int
	var failed []string
	for _, name := range names {
		run := cli.Verify{
			Store: archive, Profile: name,
			From: start, To: end, JSON: asJSON,
			Instance:     record.InstanceName(),
			RequiredLock: requiredLocks(profiles),
		}
		if cfg.Seals != nil {
			run.Seals = &cli.SealCheck{Roots: cfg.Seals.Roots, Settle: cfg.Seals.Settle.D(), Grace: cfg.Seals.Grace.D()}
		}
		if cfg.Sink != nil {
			if run.Catalogue, err = catalogue.Common(); err != nil {
				return err
			}
			if run.Sink, err = cli.SinkFrom(*cfg.Sink, cfg.Require); err != nil {
				return err
			}
		}
		n, err := run.Run(ctx)
		if err != nil {
			return fmt.Errorf("profile %s: %w", name, err)
		}
		if n > 0 {
			problems += n
			failed = append(failed, name)
		}
	}
	if problems > 0 {
		return fmt.Errorf("%d problems, in %s", problems, strings.Join(failed, ", "))
	}
	return nil
}

func purgeFromConfig(path string, asJSON bool) error {
	cfg, err := config.LoadPurge(path)
	if err != nil {
		return err
	}
	profiles, err := profilesFor(cfg.Deployment)
	if err != nil {
		return err
	}
	window := dedupeFor(profiles, cfg.DedupeWindow.D())

	ctx := context.Background()
	poolConfig, err := cfg.Database.PoolConfig()
	if err != nil {
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := postgres.CheckVersion(ctx, pool); err != nil {
		return err
	}
	target, err := postgres.New(pool)
	if err != nil {
		return err
	}
	dedupe, err := postgres.NewDedupe(pool, window)
	if err != nil {
		return err
	}
	_, err = cli.Purge{
		Index: target, Dedupe: dedupe, Profiles: profiles,
		IdentifyingAfter: cfg.IdentifyingAfter.D(), DedupeWindow: window, JSON: asJSON,
	}.Run(ctx)
	return err
}

func clockSyncFromConfig(path string, asJSON bool) error {
	cfg, err := config.LoadClockSync(path)
	if err != nil {
		return err
	}
	common, err := catalogue.Common()
	if err != nil {
		return err
	}
	run := cli.ClockSync{
		Catalogue: common, Servers: cfg.NTP, MaxOffset: cfg.MaxOffset.D(),
		Timeout: cfg.Timeout.D(), Version: buildinfo.Version,
		Instance: record.InstanceName(), JSON: asJSON,
	}
	if cfg.Sink != nil {
		if run.Sink, err = cli.SinkFrom(*cfg.Sink, cfg.Require); err != nil {
			return err
		}
	}
	_, err = run.Run(context.Background())
	return err
}

func migrateFromConfig(path string) error {
	cfg, err := config.LoadMigrate(path)
	if err != nil {
		return err
	}
	ctx := context.Background()
	poolConfig, err := cfg.Database.PoolConfig()
	if err != nil {
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	return applyMigration(ctx, pool, postgres.Roles{
		Writer: cfg.Writer, Observe: cfg.Observe, Reader: cfg.Reader, Purge: cfg.Purge,
	})
}
