package writer

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func logged(missing, kept []string) string {
	var out bytes.Buffer
	log := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	reportUnhandled(log, "audit.digest.written", missing, kept)
	return out.String()
}

// A profile only some deployments configure is not a fault when another
// profile the action names keeps its records.
func TestAPartialMatchIsNotAWarning(t *testing.T) {
	got := logged([]string{"evidence"}, []string{"security"})
	if strings.Contains(got, "level=WARN") || strings.Contains(got, "level=ERROR") {
		t.Fatalf("a kept action warned: %s", got)
	}
	if !strings.Contains(got, "level=DEBUG") || !strings.Contains(got, "evidence") {
		t.Fatalf("the partial match is not a debug line naming what is missing: %s", got)
	}
}

// When none of the profiles is configured the records are dropped, and the
// warning names the action and the profiles nobody configured.
func TestNoMatchWarnsNamingTheAction(t *testing.T) {
	got := logged([]string{"evidence", "security"}, nil)
	if !strings.Contains(got, "level=WARN") || !strings.Contains(got, "audit.digest.written") ||
		!strings.Contains(got, "evidence") {
		t.Fatalf("a dropped action did not warn by name: %s", got)
	}
}
