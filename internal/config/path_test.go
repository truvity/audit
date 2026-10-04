package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/audit/internal/config"
)

func touch(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("x: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestThePathIsTheFlagThenTheVariableThenTheFirstDefaultThatExists(t *testing.T) {
	dir := t.TempDir()
	layer, legacy := touch(t, dir, "layer.yaml"), touch(t, dir, "legacy.yaml")
	missing := filepath.Join(dir, "missing.yaml")

	t.Setenv(config.EnvConfig, "/from/env.yaml")
	if got, err := config.Path("/from/flag.yaml", "audit-writer", layer); err != nil || got != "/from/flag.yaml" {
		t.Errorf("flag and variable: got %q, %v; the flag is the more specific", got, err)
	}
	if got, err := config.Path("", "audit-writer", layer); err != nil || got != "/from/env.yaml" {
		t.Errorf("variable and a default: got %q, %v; the variable names a file, the default only guesses", got, err)
	}

	t.Setenv(config.EnvConfig, "")
	if got, err := config.Path("", "audit-writer-lambda", layer, legacy); err != nil || got != layer {
		t.Errorf("both defaults: got %q, %v", got, err)
	}
	if got, err := config.Path("", "audit-writer-lambda", missing, legacy); err != nil || got != legacy {
		t.Errorf("only the old place: got %q, %v; the previous release's layout still starts", got, err)
	}
}

func TestNoPathAtAllIsARefusalThatSaysHowToGiveOne(t *testing.T) {
	t.Setenv(config.EnvConfig, "  ")
	_, err := config.Path("", "audit-query")
	if err == nil {
		t.Fatal("a process with no configuration file started")
	}
	for _, want := range []string{"--config", config.EnvConfig, "audit-query.schema.json"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should say %q: %v", want, err)
		}
	}
	_, err = config.Path("", "audit-writer-lambda", "/opt/audit/audit.yaml", "/var/task/audit.yaml")
	if err == nil || !strings.Contains(err.Error(), "/opt/audit/audit.yaml") {
		t.Errorf("a Lambda with nothing mounted should be told where the file goes: %v", err)
	}
}
