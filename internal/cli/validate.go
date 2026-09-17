package cli

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/preset"
)

// Validate holds every preset and catalogue under the given paths to the
// contracts this repository publishes, and reports everything wrong at once
// rather than the first thing.
type Validate struct {
	PresetDirs   []string
	CatalogueDoc []string
	Deployment   string
	Out          io.Writer
}

// Run reports the number of problems found.
func (v Validate) Run() (problems int) {
	out := v.Out
	if out == nil {
		out = os.Stdout
	}
	presets, err := preset.Builtin()
	if err != nil {
		printf(out, "presets (built in): %v\n", err)
		problems++
		presets = map[string]*preset.Preset{}
	} else {
		printf(out, "presets (built in): %d loaded\n", len(presets))
	}
	for _, dir := range v.PresetDirs {
		loaded, err := preset.LoadDir(os.DirFS(dir), ".")
		if err != nil {
			printf(out, "presets %s: %v\n", dir, err)
			problems++
			continue
		}
		printf(out, "presets %s: %d loaded\n", dir, len(loaded))
		for name, p := range loaded {
			presets[name] = p
		}
	}

	var catalogues []*catalogue.Catalogue
	for _, doc := range v.CatalogueDoc {
		c, err := catalogue.LoadFS(os.DirFS(filepath.Dir(doc)), filepath.Base(doc))
		if err != nil {
			printf(out, "catalogue %s:\n%s\n", doc, indent(err.Error()))
			problems++
			continue
		}
		catalogues = append(catalogues, c)
		printf(out, "catalogue %s: source %s version %s, %d actions\n",
			doc, c.Source, c.Version, len(c.ActionNames()))
	}

	// A profile may claim to satisfy a framework only if something in the
	// installation records the events that framework asks for. Without a
	// deployment there is nothing to check that against.
	if v.Deployment == "" {
		return problems
	}
	d, err := LoadDeployment(v.Deployment)
	if err != nil {
		printf(out, "deployment: %v\n", err)
		return problems + 1
	}
	profiles, err := d.Compose(presets)
	if err != nil {
		printf(out, "deployment: %v\n", err)
		return problems + 1
	}
	names := make([]string, 0, len(profiles))
	for n := range profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		p := profiles[name]
		missing := catalogue.MissingCategories(name, p.RequiredCategories, catalogues)
		if len(missing) == 0 {
			printf(out, "profile %s: composed, every required category is covered\n", name)
			continue
		}
		problems++
		printf(out, "profile %s: nothing emits these categories it requires: %s\n",
			name, strings.Join(missing, ", "))
	}
	return problems
}

// FindCatalogues returns every catalogue document under a directory.
func FindCatalogues(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, ".yaml") && (name == "catalogue.yaml" || strings.HasPrefix(name, "catalogue")) {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// printf writes a line of the report. A command that cannot write its own
// output has nothing left to report the failure with, so the error is dropped
// here rather than threaded through every caller.
func printf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}
