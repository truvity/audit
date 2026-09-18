// Package docscheck holds the documentation to what it points at.
package docscheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// root is the repository, two levels above this package.
func root(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

var (
	link    = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	heading = regexp.MustCompile(`(?m)^#{1,6}\s+(.+?)\s*$`)
	fence   = regexp.MustCompile("(?s)```.*?```")
)

// slug is the anchor GitHub gives a heading: lower case, punctuation other
// than hyphens and underscores removed, spaces to hyphens.
func slug(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	var b strings.Builder
	for _, r := range text {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || r == '_':
			b.WriteRune(r)
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r > 127:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func anchors(t *testing.T, path string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := fence.ReplaceAllString(string(raw), "")
	out := map[string]bool{}
	for _, m := range heading.FindAllStringSubmatch(text, -1) {
		// Headings may carry inline code or links; the anchor is from the text.
		h := strings.NewReplacer("`", "", "*", "").Replace(m[1])
		h = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`).ReplaceAllString(h, "$1")
		out[slug(h)] = true
	}
	return out
}

// Every relative link in the documentation points at a file that exists and,
// for a Markdown file, at a heading it has. A page that sends a reader to
// something moved or renamed is the documentation failing quietly.
func TestRelativeLinksResolve(t *testing.T) {
	base := root(t)
	var pages []string
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", ".devbox", "dist", "gen":
				return filepath.SkipDir
			}
			return nil
		}
		// The decision template's links are placeholders by design.
		if strings.HasSuffix(path, ".md") && d.Name() != "0000-template.md" {
			pages = append(pages, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cache := map[string]map[string]bool{}
	checked := 0
	for _, page := range pages {
		raw, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		text := fence.ReplaceAllString(string(raw), "")
		for _, m := range link.FindAllStringSubmatch(text, -1) {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			file, anchor, _ := strings.Cut(target, "#")
			resolved := page
			if file != "" {
				resolved = filepath.Join(filepath.Dir(page), file)
			}
			rel, _ := filepath.Rel(base, page)
			if _, err := os.Stat(resolved); err != nil {
				t.Errorf("%s: %s does not exist", rel, target)
				continue
			}
			checked++
			if anchor == "" || !strings.HasSuffix(resolved, ".md") {
				continue
			}
			if cache[resolved] == nil {
				cache[resolved] = anchors(t, resolved)
			}
			if !cache[resolved][anchor] {
				t.Errorf("%s: %s has no heading #%s", rel, file, anchor)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no links were checked; the walk found nothing")
	}
}
