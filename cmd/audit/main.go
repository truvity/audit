// Command audit is the tool that holds a deployment to the contracts this
// repository publishes: it validates catalogues and presets, explains what a
// profile keeps, and checks that the code and the catalogue still agree.
//
// It runs in an application's own tests, so that a catalogue is wrong in a pull
// request rather than in an archive nobody can rewrite.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/record"
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
