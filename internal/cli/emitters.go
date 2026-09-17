package cli

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/truvity/audit/catalogue"
)

// CheckEmitters finds the action names a source's code emits and holds them to
// the catalogue that describes them.
//
// It reads string literals rather than resolving calls, so it catches the
// mistakes that actually happen — a typo, an action added in code and not in
// the catalogue, an action retired from the catalogue and still emitted — and
// says plainly that it cannot see a name assembled at run time. A catalogue
// that is only as true as the code remembers to keep it is not a contract.
type CheckEmitters struct {
	Root      string
	Catalogue string
	Out       io.Writer
}

var sourceFile = regexp.MustCompile(`\.(go|ts|tsx|js|mjs)$`)

// Run reports the number of problems found.
func (c CheckEmitters) Run() int {
	out := c.Out
	if out == nil {
		out = os.Stdout
	}
	cat, err := catalogue.LoadFS(os.DirFS(filepath.Dir(c.Catalogue)), filepath.Base(c.Catalogue))
	if err != nil {
		printf(out, "catalogue %s: %v\n", c.Catalogue, err)
		return 1
	}
	declared := map[string]bool{}
	for _, name := range cat.ActionNames() {
		declared[name] = false // false means "declared but not seen in the code yet"
	}

	// A literal under this source's namespace, long enough to be an action.
	literal := regexp.MustCompile(`"(` + regexp.QuoteMeta(cat.Source) + `\.[a-z][a-z0-9_.-]*)"`)
	found := map[string][]string{}
	err = filepath.WalkDir(c.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "dist", "gen", "vendor":
				return fs.SkipDir
			}
			return nil
		}
		if !sourceFile.MatchString(d.Name()) || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range literal.FindAllSubmatch(body, -1) {
			name := string(m[1])
			found[name] = append(found[name], path)
		}
		return nil
	})
	if err != nil {
		printf(out, "walk %s: %v\n", c.Root, err)
		return 1
	}

	problems := 0
	names := make([]string, 0, len(found))
	for n := range found {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := declared[name]; !ok {
			problems++
			printf(out, "%s is emitted but catalogue %s does not declare it (%s)\n",
				name, cat.Source, strings.Join(unique(found[name]), ", "))
			continue
		}
		declared[name] = true
	}
	for _, name := range cat.ActionNames() {
		if !declared[name] {
			printf(out, "%s is declared but nothing emits it\n", name)
		}
	}
	printf(out, "%d actions declared, %d emitted, %d problems\n", len(cat.ActionNames()), len(names), problems)
	return problems
}

func unique(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
