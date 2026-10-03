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

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/truvity/audit/index/postgres"
	"github.com/truvity/audit/internal/buildinfo"
	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/internal/config"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/sdk/catalogue"
	"github.com/truvity/audit/sdk/record"
)

const configUsage = "the configuration file; with it, nothing else configures the command"

// onlyConfig refuses a command line that names a configuration file and also
// configures the command another way: two answers to one question, and the
// file is the one that is reviewed. --json only changes how the report is
// printed, so it may stay.
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

func digestFromConfig(path string, asJSON bool) error {
	cfg, err := config.LoadDigest(path)
	if err != nil {
		return err
	}
	profiles, err := profilesFor(cfg.Deployment)
	if err != nil {
		return err
	}
	// The digests land in the same store as the copies they cover, so the job
	// is held to the same refusal as the writer -- before a signer is opened,
	// since a KMS or transit signer is a round trip of its own.
	if err := preset.CheckLockMode(profiles, cfg.Archive.LockMode); err != nil {
		return err
	}

	ctx := context.Background()
	var signer keys.Signer
	switch s := cfg.Signer; {
	case s.KMSKey != "":
		var opts []func(*awsconfig.LoadOptions) error
		if r := cfg.Archive.Bucket.Region; r != "" {
			opts = append(opts, awsconfig.WithRegion(r))
		}
		aws, err := awsconfig.LoadDefaultConfig(ctx, opts...)
		if err != nil {
			return err
		}
		signer = &keys.KMSSigner{Client: kms.NewFromConfig(aws), Key: s.KMSKey}
	case s.Transit != nil:
		if signer, err = cli.TransitSignerFrom(ctx, *s.Transit); err != nil {
			return err
		}
	default:
		if signer, err = keys.LoadLocalSignerFile(s.KeyFile.ID, s.KeyFile.Path); err != nil {
			return err
		}
	}

	run := cli.Digest{
		Profiles: profiles, Signer: signer,
		Lookback: cfg.Lookback.D(), MaxWindows: cfg.MaxWindows, JSON: asJSON,
		Instance: record.InstanceName(),
	}
	if cfg.Sink != nil {
		if run.Catalogue, err = catalogue.Common(); err != nil {
			return err
		}
		if run.Sink, err = cli.SinkFrom(*cfg.Sink, cfg.Require); err != nil {
			return err
		}
	}
	if run.Store, err = cli.OpenArchiveFrom(ctx, cfg.Archive); err != nil {
		return err
	}
	_, err = run.Run(ctx)
	return err
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
	pem, err := os.ReadFile(cfg.PublicKeyFile)
	if err != nil {
		return fmt.Errorf("publicKeyFile: %w", err)
	}

	ctx := context.Background()
	archive, err := cli.OpenArchiveFrom(ctx, cfg.Archive)
	if err != nil {
		return err
	}
	// A job that writes its results into the archive is held to the same
	// refusal as the writer.
	if err := preset.CheckLockMode(profiles, string(archive.Lock())); err != nil {
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
			Store: archive, PublicKeyPEM: pem, Profile: name,
			From: start, To: end, Lookback: cfg.Lookback.D(), JSON: asJSON,
			Instance: record.InstanceName(), Record: cfg.Record,
			RequiredLock: requiredLocks(profiles),
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
	return applyMigration(ctx, pool, cfg.Reader)
}
