package docscheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// maxPageLines is the length above which a documentation page is likely doing
// two jobs. It warns and does not fail: a long reference table is legitimate.
const maxPageLines = 400

var (
	regionOpen  = regexp.MustCompile(`^<!-- generated: ([a-z0-9-]+) -->\s*$`)
	regionClose = regexp.MustCompile(`^<!-- /generated -->\s*$`)
)

func docPages(t *testing.T) []string {
	t.Helper()
	var pages []string
	err := filepath.WalkDir(filepath.Join(root(t), "docs"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".md") {
			pages = append(pages, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) == 0 {
		t.Fatal("no documentation pages found; the check compared nothing")
	}
	return pages
}

// TestPageLength warns about a page over maxPageLines. CHANGELOG.md lives at
// the repository root and is not under docs/, so it is exempt by position.
func TestPageLength(t *testing.T) {
	for _, page := range docPages(t) {
		raw, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(raw), "\n"); n > maxPageLines {
			t.Logf("WARNING: %s has %d lines (over %d); consider splitting it", page, n, maxPageLines)
		}
	}
}

// TestGeneratedRegionsClosed fails on a `<!-- generated: name -->` region that
// is not closed by `<!-- /generated -->` before the next one opens or the page
// ends, and on a close with no open.
func TestGeneratedRegionsClosed(t *testing.T) {
	for _, page := range docPages(t) {
		raw, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		open := ""
		for i, line := range strings.Split(string(raw), "\n") {
			switch {
			case regionOpen.MatchString(line):
				if open != "" {
					t.Errorf("%s:%d: region %q opens inside the unclosed region %q", page, i+1, regionOpen.FindStringSubmatch(line)[1], open)
				}
				open = regionOpen.FindStringSubmatch(line)[1]
			case regionClose.MatchString(line):
				if open == "" {
					t.Errorf("%s:%d: <!-- /generated --> with no open region", page, i+1)
				}
				open = ""
			}
		}
		if open != "" {
			t.Errorf("%s: region %q is never closed", page, open)
		}
	}
}
