// Command audit is the tool that holds a deployment to the contracts this
// repository publishes: it validates catalogues and presets, explains what a
// profile keeps, and checks that the code and the catalogue still agree.
//
// It runs in an application's own tests, so that a catalogue is wrong in a pull
// request rather than in an archive nobody can rewrite.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/index/postgres"
	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/store/s3store"
)

const usage = `audit — the audit trail toolchain

usage:
  audit validate [flags] [catalogue.yaml ...]
        Hold presets, catalogues and a deployment's profiles to their contracts.

  audit profile explain <name> [flags]
        Print what a profile keeps, how it treats identities, and how long it
        is kept.

  audit check-emitters <dir> --catalogue <file>
        Check that the actions the code emits are the actions the catalogue
        declares.

  audit verify --profile <name> --from <date> --to <date> [flags]
        Walk a profile's digest chain and report what it finds. Needs the
        archive and a public key, and nothing that has to be trusted.

  audit replay --dlq --from <date> --to <date> [flags]
        Send dead letters back to a writer once the cause is fixed. Without
        --sink it reads and summarises them and sends nothing.

  audit digest --deployment <file> --key <file> [flags]
        Seal the windows since the last digest into the signed chain. Run it
        hourly. It catches up on windows a missed run left behind, because a
        gap in the chain cannot be told from a digest somebody removed.

  audit purge --deployment <file> --database <url> [flags]
        Bring the index and the deduplication table back within what the
        profiles allow. It never touches the archive: those objects are
        released by their object lock, not by this.

  audit clock-sync --ntp <server> [--sink <url>] [flags]
        Compare this machine's clock with UTC and record the answer. Run it
        daily. It does not set the clock: whatever runs the machine does that,
        and recording the times of things is a separate job from setting them.

  audit key destroy --tenant <id> --purpose <p> --by <who> [flags]
        Destroy a tenant's pseudonymisation key. The copies stay and their
        pseudonyms can never be recomputed again: this is what erasure means
        here, and it cannot be undone.

  audit hold place|release|list [flags]
        Place a legal hold on a profile's copies, or a tenant's within it, and
        record who did and why. A hold keeps objects undeletable whatever
        their retention says, until somebody takes it off.

  audit migrate --database <url>
        Apply the index schema. Run it before the writers that will use it,
        and run it from one place: several replicas migrating at once is a
        race the writers cannot see.

  audit reindex --profile <name> --from <date> --to <date> [flags]
        Rebuild a profile's index from the archive. Safe to run over a range
        that is already indexed, and the way an index is repaired or replaced.

  audit version

Run a command with -h for its flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "validate":
		err = validate(os.Args[2:])
	case "profile":
		err = profile(os.Args[2:])
	case "check-emitters":
		err = checkEmitters(os.Args[2:])
	case "verify":
		err = verify(os.Args[2:])
	case "replay":
		err = replay(os.Args[2:])
	case "key":
		err = keyCmd(os.Args[2:])
	case "hold":
		err = holdCmd(os.Args[2:])
	case "clock-sync":
		err = clockSync(os.Args[2:])
	case "digest":
		err = digestCmd(os.Args[2:])
	case "purge":
		err = purge(os.Args[2:])
	case "migrate":
		err = migrate(os.Args[2:])
	case "reindex":
		err = reindex(os.Args[2:])
	case "version":
		fmt.Printf("audit, record schema %s\n", record.SchemaVersion)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "audit: no command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit: %v\n", err)
		os.Exit(1)
	}
}

func validate(args []string) error {
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	var presets stringList
	flags.Var(&presets, "presets", "directory of preset files, repeatable")
	deployment := flags.String("deployment", "", "a deployment's profile configuration")
	scan := flags.String("scan", "", "find catalogue documents under this directory")
	docs, err := parse(flags, args)
	if err != nil {
		return err
	}
	if *scan != "" {
		found, err := cli.FindCatalogues(*scan)
		if err != nil {
			return err
		}
		docs = append(docs, found...)
	}
	v := cli.Validate{PresetDirs: presets, CatalogueDoc: docs, Deployment: *deployment}
	if problems := v.Run(); problems > 0 {
		return fmt.Errorf("%d problems", problems)
	}
	return nil
}

func profile(args []string) error {
	if len(args) == 0 || args[0] != "explain" {
		return fmt.Errorf("usage: audit profile explain <name> [--deployment file]")
	}
	flags := flag.NewFlagSet("profile explain", flag.ContinueOnError)
	deployment := flags.String("deployment", "", "a deployment's profile configuration")
	names, err := parse(flags, args[1:])
	if err != nil {
		return err
	}
	presets, err := preset.Builtin()
	if err != nil {
		return err
	}
	d := cli.DefaultDeployment(presets)
	if *deployment != "" {
		if d, err = cli.LoadDeployment(*deployment); err != nil {
			return err
		}
	}
	profiles, err := d.Compose(presets)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		available := make([]string, 0, len(profiles))
		for n := range profiles {
			available = append(available, n)
		}
		sort.Strings(available)
		return fmt.Errorf("name a profile: %s", strings.Join(available, ", "))
	}
	p, ok := profiles[names[0]]
	if !ok {
		return fmt.Errorf("no profile %q in this deployment", names[0])
	}
	fmt.Print(p.Explain())
	return nil
}

func checkEmitters(args []string) error {
	flags := flag.NewFlagSet("check-emitters", flag.ContinueOnError)
	cat := flags.String("catalogue", "", "the catalogue the code is held to")
	dirs, err := parse(flags, args)
	if err != nil {
		return err
	}
	if len(dirs) != 1 || *cat == "" {
		return fmt.Errorf("usage: audit check-emitters <dir> --catalogue <file>")
	}
	c := cli.CheckEmitters{Root: dirs[0], Catalogue: *cat}
	if problems := c.Run(); problems > 0 {
		return fmt.Errorf("%d problems", problems)
	}
	return nil
}

func verify(args []string) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	var (
		profile   = flags.String("profile", "", "the profile whose chain to walk")
		from      = flags.String("from", "", "start of the range, a date or a timestamp")
		to        = flags.String("to", "", "end of the range, a date or a timestamp")
		publicKey = flags.String("public-key", "", "the PEM public key the digests were signed with")
		bucket    = flags.String("bucket", "", "the bucket the archive is in")
		prefix    = flags.String("prefix", "", "the prefix within the bucket")
		region    = flags.String("region", "", "the region, when it is not in the environment")
		lookback  = flags.Duration("lookback", 0,
			"how far before the range to look for objects keyed under an older day; at least what audit digest used")
		last = flags.Duration("last", 0,
			"check the windows of the last this long, ending at the hour that has closed; instead of --from and --to")
		sinkURL  = flags.String("sink", "", "the writer this job records what it checked through")
		instance = flags.String("instance", "", "the name this job records itself under")
		record   = flags.Bool("record", false,
			"write a verification per window into the archive, which a record's provenance reads; needs write access to verified/")
		asJSON = flags.Bool("json", false, "print the report as JSON")
	)
	if _, err := parse(flags, args); err != nil {
		return err
	}
	switch {
	case *profile == "":
		return errors.New("name a profile with --profile")
	case *publicKey == "":
		return errors.New("give the signing key's public half with --public-key")
	case *bucket == "":
		return errors.New("name the archive's bucket with --bucket")
	}
	// A scheduled run says "the last day"; an auditor names the range. The
	// image the jobs run from has no shell, so the arithmetic lives here.
	var start, end time.Time
	var err error
	switch {
	case *last > 0 && (*from != "" || *to != ""):
		return errors.New("give --last, or --from and --to, not both")
	case *last > 0:
		end = time.Now().UTC().Truncate(time.Hour)
		start = end.Add(-*last)
	default:
		if start, err = cli.ParseDay(*from); err != nil {
			return fmt.Errorf("--from: %w", err)
		}
		if end, err = cli.ParseDay(*to); err != nil {
			return fmt.Errorf("--to: %w", err)
		}
	}
	pem, err := os.ReadFile(*publicKey)
	if err != nil {
		return err
	}

	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return err
	}
	if *region != "" {
		cfg.Region = *region
	}
	archive, err := s3store.FromConfig(cfg, s3store.Options{Bucket: *bucket, Prefix: *prefix})
	if err != nil {
		return err
	}

	run := cli.Verify{
		Store: archive, PublicKeyPEM: pem, Profile: *profile,
		From: start, To: end, Lookback: *lookback, JSON: *asJSON,
		Instance: *instance, Record: *record,
	}
	if *sinkURL != "" {
		if run.Catalogue, err = catalogue.Common(); err != nil {
			return err
		}
		run.Sink = cli.WriterClient(*sinkURL)
	}
	problems, err := run.Run(ctx)
	if err != nil {
		return err
	}
	if problems > 0 {
		return fmt.Errorf("%d problems", problems)
	}
	return nil
}

// parse takes flags and arguments in any order. The standard library stops at
// the first argument, which turns a misplaced flag into a file path and a
// confusing error; a command should not care where its flags were typed.
func parse(flags *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for len(args) > 0 {
		if err := flags.Parse(args); err != nil {
			return nil, err
		}
		rest := flags.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
	return positional, nil
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func replay(args []string) error {
	flags := flag.NewFlagSet("replay", flag.ContinueOnError)
	var (
		dlq = flags.Bool("dlq", false,
			"replay the dead-letter prefix; naming the source is required so that a later one cannot become the default")
		from    = flags.String("from", "", "start of the range, a date or a timestamp")
		to      = flags.String("to", "", "end of the range, a date or a timestamp")
		reason  = flags.String("reason", "", "keep only dead letters whose reason contains this text")
		action  = flags.String("action", "", "keep only dead letters of this action")
		sinkURL = flags.String("sink", "", "the writer's base URL; without it nothing is sent")
		bucket  = flags.String("bucket", "", "the bucket the archive is in")
		prefix  = flags.String("prefix", "", "the prefix within the bucket")
		region  = flags.String("region", "", "the region, when it is not in the environment")
		batch   = flags.Int("batch", 100, "how many records to send at a time")
		asJSON  = flags.Bool("json", false, "print the report as JSON")
	)
	if _, err := parse(flags, args); err != nil {
		return err
	}
	switch {
	case !*dlq:
		return errors.New("name the source with --dlq")
	case *bucket == "":
		return errors.New("name the archive's bucket with --bucket")
	}
	start, err := cli.ParseDay(*from)
	if err != nil {
		return fmt.Errorf("--from: %w", err)
	}
	end, err := cli.ParseDay(*to)
	if err != nil {
		return fmt.Errorf("--to: %w", err)
	}

	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return err
	}
	if *region != "" {
		cfg.Region = *region
	}
	archive, err := s3store.FromConfig(cfg, s3store.Options{Bucket: *bucket, Prefix: *prefix})
	if err != nil {
		return err
	}

	r := cli.Replay{
		Store: archive, From: start, To: end,
		Reason: *reason, Action: *action, Batch: *batch,
	}
	if *sinkURL != "" {
		r.Sink = cli.WriterClient(*sinkURL)
	}
	report, err := r.Run(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		body, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Printf("%s\n", body)
	}
	if report.DeadEnd > 0 {
		return fmt.Errorf("%d records could not be processed and are back under the dead-letter prefix", report.DeadEnd)
	}
	return nil
}

// migrate applies the index schema.
//
// It is a command of its own rather than something a writer does on start-up
// because several replicas migrating at once is a race, and because a schema
// change to the index should be a step an operator takes deliberately. Nothing
// in it touches the archive: the index is a projection, and this is the
// database that holds it.
func migrate(args []string) error {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	var (
		database = flags.String("database", "", "the Postgres URL of the index")
		printSQL = flags.Bool("print", false, "print the schema and apply nothing")
	)
	if _, err := parse(flags, args); err != nil {
		return err
	}
	if *printSQL {
		fmt.Print(postgres.Schema())
		return nil
	}
	if *database == "" {
		return errors.New("give the index's Postgres URL with --database, or use --print")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *database)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := postgres.Migrate(ctx, pool); err != nil {
		return err
	}
	fmt.Printf("index schema version %d applied\n", postgres.Version)
	return nil
}

// reindex rebuilds a profile's index from the archive.
func reindex(args []string) error {
	flags := flag.NewFlagSet("reindex", flag.ContinueOnError)
	var (
		profile   = flags.String("profile", "", "the profile to rebuild")
		from      = flags.String("from", "", "start of the range, a date or a timestamp")
		to        = flags.String("to", "", "end of the range, a date or a timestamp")
		database  = flags.String("database", "", "the Postgres URL of the index")
		bucket    = flags.String("bucket", "", "the bucket the archive is in")
		prefix    = flags.String("prefix", "", "the prefix within the bucket")
		region    = flags.String("region", "", "the region, when it is not in the environment")
		batchSize = flags.Int("batch", 0, "how many rows to index at a time")
		asJSON    = flags.Bool("json", false, "print the report as JSON")
	)
	var catalogueFiles repeated
	flags.Var(&catalogueFiles, "catalogue",
		"a catalogue document, repeatable; the index takes its indexed properties from these")
	if _, err := parse(flags, args); err != nil {
		return err
	}
	switch {
	case *profile == "":
		return errors.New("name a profile with --profile")
	case *database == "":
		return errors.New("give the index's Postgres URL with --database")
	case *bucket == "":
		return errors.New("name the archive's bucket with --bucket")
	case len(catalogueFiles) == 0:
		return errors.New(
			"give the catalogues with --catalogue: without them the index would be " +
				"rebuilt without its data columns, and a later run could not repair it")
	}
	start, err := cli.ParseDay(*from)
	if err != nil {
		return fmt.Errorf("--from: %w", err)
	}
	end, err := cli.ParseDay(*to)
	if err != nil {
		return fmt.Errorf("--to: %w", err)
	}
	fields, err := cli.CatalogueFields(catalogueFiles)
	if err != nil {
		return err
	}

	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return err
	}
	if *region != "" {
		cfg.Region = *region
	}
	archive, err := s3store.FromConfig(cfg, s3store.Options{Bucket: *bucket, Prefix: *prefix})
	if err != nil {
		return err
	}

	pool, err := pgxpool.New(ctx, *database)
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

	_, err = cli.Reindex{
		Store: archive, Index: target, Fields: fields,
		Profile: *profile, From: start, To: end,
		Batch: *batchSize, JSON: *asJSON,
	}.Run(ctx)
	return err
}

// repeated collects a flag that may be given more than once, which is how a
// deployment names its catalogues: it has several, and they are separate files.
type repeated []string

func (r *repeated) String() string     { return strings.Join(*r, ", ") }
func (r *repeated) Set(v string) error { *r = append(*r, v); return nil }

// archiveFor opens the archive a command reads or writes.
func archiveFor(ctx context.Context, bucket, prefix, region string) (*s3store.Store, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	if region != "" {
		cfg.Region = region
	}
	return s3store.FromConfig(cfg, s3store.Options{Bucket: bucket, Prefix: prefix})
}

// profilesFor composes the deployment's profiles, which is what says how long
// anything is kept.
func profilesFor(path string) (map[string]*preset.Profile, error) {
	presets, err := preset.Builtin()
	if err != nil {
		return nil, err
	}
	d, err := cli.LoadDeployment(path)
	if err != nil {
		return nil, err
	}
	return d.Compose(presets)
}

// digestCmd seals windows into the signed chain.
func digestCmd(args []string) error {
	flags := flag.NewFlagSet("digest", flag.ContinueOnError)
	var (
		deployment = flags.String("deployment", "", "the profile configuration")
		only       = flags.String("profile", "", "seal only this profile; default every one")
		key        = flags.String("key", "", "PEM private key the digests are signed with")
		keyID      = flags.String("key-id", "", "the name a digest records the signing key under")
		from       = flags.String("from", "", "first window; default the hour after the last digest")
		to         = flags.String("to", "", "last window; default the hour that has just closed")
		bucket     = flags.String("bucket", "", "the bucket the archive is in")
		prefix     = flags.String("prefix", "", "the prefix within the bucket")
		region     = flags.String("region", "", "the region, when it is not in the environment")
		lookback   = flags.Duration("lookback", 0, "how far back to look for objects keyed under an older day")
		maxWindows = flags.Int("max-windows", 0, "how many windows one run may seal")
		sinkURL    = flags.String("sink", "", "the writer this job records what it sealed through")
		instance   = flags.String("instance", "", "the name this job records itself under")
		asJSON     = flags.Bool("json", false, "print the report as JSON")
	)
	if _, err := parse(flags, args); err != nil {
		return err
	}
	switch {
	case *deployment == "":
		return errors.New("give the profile configuration with --deployment")
	case *bucket == "":
		return errors.New("name the archive's bucket with --bucket")
	case *key == "":
		return errors.New("give the signing key with --key: an unsigned chain proves nothing")
	}

	profiles, err := profilesFor(*deployment)
	if err != nil {
		return err
	}
	if *only != "" {
		p, ok := profiles[*only]
		if !ok {
			return fmt.Errorf("the deployment has no profile %q", *only)
		}
		profiles = map[string]*preset.Profile{*only: p}
	}
	signer, err := keys.LoadLocalSignerFile(*keyID, *key)
	if err != nil {
		return err
	}

	run := cli.Digest{
		Profiles: profiles, Signer: signer,
		Lookback: *lookback, MaxWindows: *maxWindows, JSON: *asJSON,
		Instance: *instance,
	}
	if *sinkURL != "" {
		if run.Catalogue, err = catalogue.Common(); err != nil {
			return err
		}
		run.Sink = cli.WriterClient(*sinkURL)
	}
	if *from != "" {
		if run.From, err = cli.ParseDay(*from); err != nil {
			return fmt.Errorf("--from: %w", err)
		}
	}
	if *to != "" {
		if run.To, err = cli.ParseDay(*to); err != nil {
			return fmt.Errorf("--to: %w", err)
		}
	}

	ctx := context.Background()
	if run.Store, err = archiveFor(ctx, *bucket, *prefix, *region); err != nil {
		return err
	}
	_, err = run.Run(ctx)
	return err
}

// purge brings the index and the deduplication table within the profiles.
func purge(args []string) error {
	flags := flag.NewFlagSet("purge", flag.ContinueOnError)
	var (
		deployment  = flags.String("deployment", "", "the profile configuration")
		database    = flags.String("database", "", "the Postgres URL of the index")
		identifying = flags.Duration("identifying-after", 0,
			"how long the index keeps who an event happened to; your policy, as no shipped preset states one")
		dedupeWindow = flags.Duration("dedupe-window", 0,
			"how long a written identifier is remembered; default the widest the profiles ask for")
		dryRun = flags.Bool("dry-run", false, "report what would be purged and purge nothing")
		asJSON = flags.Bool("json", false, "print the report as JSON")
	)
	if _, err := parse(flags, args); err != nil {
		return err
	}
	switch {
	case *deployment == "":
		return errors.New("give the profile configuration with --deployment")
	case *database == "":
		return errors.New("give the index's Postgres URL with --database")
	}

	profiles, err := profilesFor(*deployment)
	if err != nil {
		return err
	}
	window := *dedupeWindow
	if window == 0 {
		for _, p := range profiles {
			if d := time.Duration(p.Pipeline.DedupeWindowDays) * 24 * time.Hour; d > window {
				window = d
			}
		}
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *database)
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
		IdentifyingAfter: *identifying, DedupeWindow: window,
		DryRun: *dryRun, JSON: *asJSON,
	}.Run(ctx)
	return err
}

// clockSync checks the clock against UTC and records what it found.
func clockSync(args []string) error {
	flags := flag.NewFlagSet("clock-sync", flag.ContinueOnError)
	var servers repeated
	flags.Var(&servers, "ntp", "a time reference, repeatable; the quickest to answer is believed")
	var (
		sinkURL   = flags.String("sink", "", "the writer the reading is recorded through")
		maxOffset = flags.Duration("max-offset", time.Second,
			"how far the clock may be out before the run fails; 0 accepts any offset and only records it")
		timeout  = flags.Duration("timeout", 5*time.Second, "how long to wait for a reference")
		instance = flags.String("instance", "", "the name this job records itself under")
		version  = flags.String("version", "dev", "this build's version")
		asJSON   = flags.Bool("json", false, "print the report as JSON")
	)
	if _, err := parse(flags, args); err != nil {
		return err
	}
	if len(servers) == 0 {
		return errors.New("name at least one time reference with --ntp")
	}

	common, err := catalogue.Common()
	if err != nil {
		return err
	}
	name := *instance
	if name == "" {
		name = record.InstanceName()
	}

	run := cli.ClockSync{
		Catalogue: common, Servers: servers, MaxOffset: *maxOffset,
		Timeout: *timeout, Version: *version, Instance: name, JSON: *asJSON,
	}
	if *sinkURL != "" {
		run.Sink = cli.WriterClient(*sinkURL)
	}
	_, err = run.Run(context.Background())
	return err
}

// holdCmd places, releases and lists legal holds.
func holdCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("audit hold needs place, release or list")
	}
	flags := flag.NewFlagSet("hold "+args[0], flag.ContinueOnError)
	var (
		profile = flags.String("profile", "", "the profile to hold")
		tenant  = flags.String("tenant", "", "narrow the hold to one tenant")
		reason  = flags.String("reason", "", "why the hold is placed; it is recorded and cannot be blank")
		id      = flags.String("id", "", "the hold's identifier")
		by      = flags.String("by", "", "who is placing or releasing it, as this deployment names them")
		bucket  = flags.String("bucket", "", "the bucket the archive is in")
		prefix  = flags.String("prefix", "", "the prefix within the bucket")
		region  = flags.String("region", "", "the region, when it is not in the environment")
		sinkURL = flags.String("sink", "", "the writer this action is recorded through")
		asJSON  = flags.Bool("json", false, "print as JSON")
	)
	if _, err := parse(flags, args[1:]); err != nil {
		return err
	}
	if *bucket == "" {
		return errors.New("name the archive's bucket with --bucket")
	}

	ctx := context.Background()
	archive, err := archiveFor(ctx, *bucket, *prefix, *region)
	if err != nil {
		return err
	}
	run := cli.Hold{
		Store: archive, Profile: *profile, Tenant: *tenant,
		Reason: *reason, ID: *id, By: *by, JSON: *asJSON,
	}
	if *sinkURL != "" {
		if run.Catalogue, err = catalogue.Common(); err != nil {
			return err
		}
		run.Sink = cli.WriterClient(*sinkURL)
	}
	return run.Run(ctx, args[0])
}

// keyCmd is the key lifecycle. Only destroy is built.
func keyCmd(args []string) error {
	if len(args) == 0 || args[0] != "destroy" {
		return errors.New("audit key needs destroy")
	}
	flags := flag.NewFlagSet("key destroy", flag.ContinueOnError)
	var (
		tenant  = flags.String("tenant", "", "the tenant whose key is destroyed")
		purpose = flags.String("purpose", "", "the purpose the key is for")
		by      = flags.String("by", "", "who is destroying it, as this deployment names them")
		reason  = flags.String("reason", "", "why, recorded with the erasure")
		keyRoot = flags.String("key-root", "", "file holding the 32-byte root the data keys are wrapped under")
		keyDir  = flags.String("key-dir", "", "where wrapped data keys are kept")
		bucket  = flags.String("bucket", "", "the bucket the archive is in, read to check for legal holds")
		prefix  = flags.String("prefix", "", "the prefix within the bucket")
		region  = flags.String("region", "", "the region, when it is not in the environment")
		sinkURL = flags.String("sink", "", "the writer the erasure is recorded through")
	)
	if _, err := parse(flags, args[1:]); err != nil {
		return err
	}
	switch {
	case *bucket == "":
		return errors.New("name the archive's bucket with --bucket: the holds are read from it")
	case *sinkURL == "":
		return errors.New("give the writer with --sink: an erasure nobody recorded is one nobody can prove was lawful")
	case *keyRoot == "":
		return errors.New("give the pseudonymisation root with --key-root")
	}

	ctx := context.Background()
	archive, err := archiveFor(ctx, *bucket, *prefix, *region)
	if err != nil {
		return err
	}
	provider, err := keyProvider(*keyRoot, *keyDir)
	if err != nil {
		return err
	}
	defer provider.Close() //nolint:errcheck // shutting down
	common, err := catalogue.Common()
	if err != nil {
		return err
	}

	return cli.KeyDestroy{
		Provider: provider, Store: archive, Sink: cli.WriterClient(*sinkURL),
		Catalogue: common, Tenant: *tenant, Purpose: *purpose, By: *by, Reason: *reason,
	}.Run(ctx)
}

// keyProvider builds the local provider from a root file.
func keyProvider(rootPath, dir string) (keys.Provider, error) {
	root, err := os.ReadFile(rootPath)
	if err != nil {
		return nil, err
	}
	return keys.NewLocal(root, dir)
}
