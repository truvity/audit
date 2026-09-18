package telemetry_test

import (
	"context"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/truvity/audit/internal/telemetry"
)

// The alert a deployment needs is on rows the index did not take, labelled by
// profile — so the counter has to exist under that name and count rows.
func TestTheWriterCountsWhatTheIndexDidNotTake(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	w, err := telemetry.NewWriter(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	if err != nil {
		t.Fatal(err)
	}
	w.Written("profile=security/tenant=acme/year=2026/month=09/day=18/a.ndjson.zst", 40)
	w.IndexDeferred("profile=security/tenant=acme/year=2026/month=09/day=18/a.ndjson.zst", 40)
	w.IndexDeferred("profile=history/tenant=acme/year=2026/month=09/day=18/b.ndjson.zst", 2)

	var got metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &got); err != nil {
		t.Fatal(err)
	}
	deferred := map[string]int64{}
	for _, scope := range got.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "audit.writer.index.deferred" {
				continue
			}
			for _, p := range m.Data.(metricdata.Sum[int64]).DataPoints {
				profile, _ := p.Attributes.Value("profile")
				deferred[profile.AsString()] += p.Value
			}
		}
	}
	if deferred["security"] != 40 || deferred["history"] != 2 {
		t.Fatalf("deferred rows by profile: %v", deferred)
	}
}

func TestProfileOf(t *testing.T) {
	for key, want := range map[string]string{
		"profile=security/tenant=acme/a.ndjson.zst":    "security",
		"prod/profile=billing-nl/tenant=acme/x.ndjson": "billing-nl",
		"holds/h-1/placed.json":                        "",
	} {
		if got := telemetry.ProfileOf(key); got != want {
			t.Errorf("%s: %q, want %q", key, got, want)
		}
	}
}
