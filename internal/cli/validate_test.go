package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/audit/preset"
)

// The repository's own presets and catalogue are the first thing the toolchain
// is held to.
func TestValidateAcceptsThisRepository(t *testing.T) {
	root := repoRoot(t)
	var out bytes.Buffer
	v := Validate{
		PresetDirs:   []string{filepath.Join(root, "presets")},
		CatalogueDoc: []string{filepath.Join(root, "catalogue", "common.yaml")},
		Out:          &out,
	}
	if problems := v.Run(); problems != 0 {
		t.Fatalf("%d problems:\n%s", problems, out.String())
	}
	if !strings.Contains(out.String(), "source audit") {
		t.Fatalf("the common catalogue was not read:\n%s", out.String())
	}
}

// A profile may claim to satisfy a framework only if something records the
// events that framework asks for. The component's own catalogue cannot cover an
// installation on its own, and saying so is the point.
func TestValidateReportsUncoveredCategories(t *testing.T) {
	root := repoRoot(t)
	deployment := filepath.Join(t.TempDir(), "deployment.yaml")
	if err := os.WriteFile(deployment, []byte("profiles:\n  security:\n    presets: [security]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	v := Validate{
		CatalogueDoc: []string{filepath.Join(root, "catalogue", "common.yaml")},
		Deployment:   deployment,
		Out:          &out,
	}
	if problems := v.Run(); problems == 0 {
		t.Fatalf("want the uncovered categories reported:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "authentication") {
		t.Fatalf("the report should name the category:\n%s", out.String())
	}
}

func TestDefaultDeploymentComposesEveryPreset(t *testing.T) {
	presets, err := preset.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := DefaultDeployment(presets).Compose(presets)
	if err != nil {
		t.Fatalf("a preset does not compose on its own: %v", err)
	}
	if len(profiles) != len(presets) {
		t.Fatalf("composed %d profiles from %d presets", len(profiles), len(presets))
	}
}

// The check reads string literals, so it finds an action the code emits that
// the catalogue does not declare.
func TestCheckEmittersFindsAnUndeclaredAction(t *testing.T) {
	root := repoRoot(t)
	dir := t.TempDir()
	code := `package app

const (
	issued  = "audit.search"
	strange = "audit.not.declared"
)
`
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c := CheckEmitters{Root: dir, Catalogue: filepath.Join(root, "catalogue", "common.yaml"), Out: &out}
	if problems := c.Run(); problems != 1 {
		t.Fatalf("problems = %d, want 1:\n%s", problems, out.String())
	}
	if !strings.Contains(out.String(), "audit.not.declared") {
		t.Fatalf("the report should name the undeclared action:\n%s", out.String())
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("cannot find the repository root")
	return ""
}

func TestParseDayAcceptsWhatAnAuditorWouldType(t *testing.T) {
	for _, in := range []string{"2026-09-17", "2026-09-17T10", "2026-09-17T10:00:00Z"} {
		at, err := ParseDay(in)
		if err != nil {
			t.Errorf("ParseDay(%q): %v", in, err)
			continue
		}
		if at.Year() != 2026 || at.Month() != 9 || at.Day() != 17 {
			t.Errorf("ParseDay(%q) = %s", in, at)
		}
	}
	if _, err := ParseDay("last tuesday"); err == nil {
		t.Error("want a refusal for something that is not a date")
	}
}
