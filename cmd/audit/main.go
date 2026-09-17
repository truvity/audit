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

	"github.com/aws/aws-sdk-go-v2/config"

	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
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
		asJSON    = flags.Bool("json", false, "print the report as JSON")
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
	start, err := cli.ParseDay(*from)
	if err != nil {
		return fmt.Errorf("--from: %w", err)
	}
	end, err := cli.ParseDay(*to)
	if err != nil {
		return fmt.Errorf("--to: %w", err)
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

	problems, err := cli.Verify{
		Store: archive, PublicKeyPEM: pem, Profile: *profile,
		From: start, To: end, JSON: *asJSON,
	}.Run(ctx)
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
		r.Sink = sink.NewClient(nil, *sinkURL)
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
