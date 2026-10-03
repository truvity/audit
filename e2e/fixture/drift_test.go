package fixture_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/truvity/audit/e2e/fixture"
)

// repoRoot resolves the checkout root relative to this source file, not the
// working directory `go test` happens to run from.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, this, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	return filepath.Join(filepath.Dir(this), "..", "..")
}

// TestChartHonoursTheFixturesNames renders charts/audit with the exact
// values file this fixture and the e2e install both read
// (charts/audit/testdata/values/e2e.yaml), and fails if the chart stops
// putting one of e2e/fixture's names where the fixture's box provisioned
// it — a chart-side rename that this package would otherwise discover only
// by installing onto the box and watching a Job fail with a missing Secret
// or a stream with no subject.
func TestChartHonoursTheFixturesNames(t *testing.T) {
	names, err := fixture.Resolve(fixture.Options{})
	if err != nil {
		t.Fatalf("resolve the fixture's names: %v", err)
	}

	root := repoRoot(t)
	cmd := exec.Command("helm", "template", names.Release,
		filepath.Join(root, "charts", "audit"),
		"--namespace", names.Namespace,
		"-f", filepath.Join(root, "charts", "audit", "testdata", "values", "e2e.yaml"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	rendered := string(out)

	// Every Secret this fixture provisions must be the one the chart asks
	// for by name, and the configuration the chart renders must carry the
	// names the box was given: the migration's reader is the query role this
	// fixture actually creates and grants — the one field here that names a
	// ROLE rather than a Secret — and the writer's config names the bucket and
	// the stream the box provisioned.
	for _, want := range []string{
		"name: " + names.WriterSecret,
		"name: " + names.S3CredsSecret,
		"reader: " + names.QueryRole,
		"observe: " + names.ObserveRole,
		"writer: " + names.WriterRole,
		"name: " + names.OwnerSecret,
		"name: " + names.ObserveSecret,
		names.ObserveRole + "@" + names.DatabaseHost,
		"name: " + names.Bucket,
		"url: " + names.StreamURL,
		"name: " + names.StreamName,
		"consumer: " + names.StreamConsumer,
		names.WriterRole + "@" + names.DatabaseHost,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the chart's render does not contain %q — the fixture and the chart have drifted:\n%s", want, rendered)
		}
	}
}
