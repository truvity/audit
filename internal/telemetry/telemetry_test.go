package telemetry_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

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

func TestTracesAreOffUnlessACollectorIsNamed(t *testing.T) {
	for _, name := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"} {
		t.Setenv(name, "")
	}
	if telemetry.TracesEnabled() || telemetry.Enabled() {
		t.Fatal("telemetry is on with no collector named")
	}
	stop, err := telemetry.Start(context.Background(), "audit-test", "v0", slog.Default())
	if err != nil || stop(context.Background()) != nil {
		t.Fatalf("a no-op start failed: %v", err)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://collector:4318")
	if !telemetry.TracesEnabled() || telemetry.Enabled() {
		t.Fatal("the traces endpoint must enable traces and not metrics")
	}
}

// Whatever a span is given, only the allowlist leaves the process: a client
// address, an error's message in an event, a status text, a link's attributes.
func TestTheExporterDropsWhatIsNotAllowlisted(t *testing.T) {
	memory := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(telemetry.FilterExporter(memory)))
	_, span := tp.Tracer("t").Start(context.Background(), "write", trace.WithAttributes(
		attribute.String("audit.action", "shop.order.placed"),
		attribute.String("audit.tenant.id", "acme"),
		attribute.String("client.address", "203.0.113.9"),
		attribute.String("audit.actor", "alice"),
		attribute.String("http.request.method", "POST"),
		attribute.String("net.peer.ip", "203.0.113.9"),
	))
	span.RecordError(errors.New("could not write the record of alice"))
	span.SetStatus(codes.Error, "alice")
	span.End()

	out := memory.GetSpans()
	if len(out) != 1 {
		t.Fatalf("%d spans exported", len(out))
	}
	for _, kv := range out[0].Attributes {
		if !telemetry.SpanAttributeAllowlist[kv.Key] {
			t.Errorf("%s left the process", kv.Key)
		}
	}
	if len(out[0].Attributes) != 3 {
		t.Errorf("attributes %v, want the three allowed", out[0].Attributes)
	}
	if len(out[0].Events) != 0 || out[0].Status.Description != "" || out[0].Status.Code != codes.Error {
		t.Errorf("events %v, status %+v", out[0].Events, out[0].Status)
	}
}

func TestTheDigestAgeIsTheTimeSinceTheNewestWindowEnded(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	clock := time.Date(2026, 10, 3, 14, 20, 0, 0, time.UTC)
	calls := 0
	err := telemetry.DigestAge(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)),
		[]string{"security", "billing"}, 24*time.Hour, 5*time.Minute,
		func(_ context.Context, p string) (time.Time, bool, error) {
			calls++
			if p == "billing" {
				return time.Time{}, false, nil
			}
			return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), true, nil
		}, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	collect := func() map[string]float64 {
		var got metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &got); err != nil {
			t.Fatal(err)
		}
		out := map[string]float64{}
		for _, sc := range got.ScopeMetrics {
			for _, m := range sc.Metrics {
				for _, p := range m.Data.(metricdata.Gauge[float64]).DataPoints {
					profile, _ := p.Attributes.Value("profile")
					out[profile.AsString()] = p.Value
				}
			}
		}
		return out
	}
	got := collect()
	if got["security"] != (2*time.Hour+20*time.Minute).Seconds() || got["billing"] != (24*time.Hour).Seconds() {
		t.Fatalf("ages %v", got)
	}
	clock = clock.Add(time.Minute)
	if got := collect(); got["security"] != (2*time.Hour+21*time.Minute).Seconds() || calls != 2 {
		t.Fatalf("a second collection inside the refresh asked the archive again (%d calls) or did not age: %v", calls, got)
	}
}

func TestTheIndexLagIsRecordedPerProfile(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	w, err := telemetry.NewWriter(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	if err != nil {
		t.Fatal(err)
	}
	w.IndexLag("profile=security/tenant=acme/x.ndjson.zst", 1500*time.Millisecond)
	var got metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &got); err != nil {
		t.Fatal(err)
	}
	for _, sc := range got.ScopeMetrics {
		for _, m := range sc.Metrics {
			if m.Name != "audit.writer.index.lag" {
				continue
			}
			p := m.Data.(metricdata.Histogram[float64]).DataPoints[0]
			if profile, _ := p.Attributes.Value("profile"); profile.AsString() != "security" || p.Count != 1 || p.Sum != 1.5 {
				t.Fatalf("%+v", p)
			}
			return
		}
	}
	t.Fatal("no audit.writer.index.lag")
}
