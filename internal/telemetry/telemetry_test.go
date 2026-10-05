package telemetry_test

import (
	"context"
	"errors"
	"fmt"
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

// The alert a deployment needs is on objects the indexer did not take, labelled
// by profile and by whether a retry will help.
func TestObserveCountsWhatItDidNotIndex(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	o, err := telemetry.NewObserve(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	if err != nil {
		t.Fatal(err)
	}
	o.Deferred("security", true)
	o.Deferred("security", false)
	o.Deferred("security", false)
	o.Deferred("history", false)

	var got metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &got); err != nil {
		t.Fatal(err)
	}
	deferred := map[string]int64{}
	for _, scope := range got.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "audit.observe.index.deferred" {
				continue
			}
			for _, p := range m.Data.(metricdata.Sum[int64]).DataPoints {
				profile, _ := p.Attributes.Value("profile")
				reason, _ := p.Attributes.Value("reason")
				deferred[profile.AsString()+"/"+reason.AsString()] += p.Value
			}
		}
	}
	if deferred["security/unreadable"] != 1 || deferred["security/retry"] != 2 || deferred["history/retry"] != 1 {
		t.Fatalf("deferred objects by profile and reason: %v", deferred)
	}
}

func TestProfileOf(t *testing.T) {
	for key, want := range map[string]string{
		"records/security/acme/2026/10/03/12/01ARZ3NDEKTSV4RRFFQ69G5FAV":   "security",
		"records/billing-nl/acme/2026/10/03/12/01ARZ3NDEKTSV4RRFFQ69G5FAV": "billing-nl",
		"holds/h-1/placed.json": "",
		"records/":              "",
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

func TestTheIndexLagIsRecordedPerProfile(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	o, err := telemetry.NewObserve(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	if err != nil {
		t.Fatal(err)
	}
	o.Indexed("security", 3, 150*time.Second)
	var got metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &got); err != nil {
		t.Fatal(err)
	}
	for _, sc := range got.ScopeMetrics {
		for _, m := range sc.Metrics {
			if m.Name != "audit.observe.index.lag" {
				continue
			}
			p := m.Data.(metricdata.Histogram[float64]).DataPoints[0]
			if profile, _ := p.Attributes.Value("profile"); profile.AsString() != "security" || p.Count != 1 || p.Sum != 150 {
				t.Fatalf("%+v", p)
			}
			return
		}
	}
	t.Fatal("no audit.observe.index.lag")
}

func TestTheDefaultSamplerKeepsEveryTrace(t *testing.T) {
	sampler := telemetry.DefaultSampler()
	want := "ParentBased{root:AlwaysOnSampler," +
		"remoteParentSampled:AlwaysOnSampler,remoteParentNotSampled:AlwaysOffSampler," +
		"localParentSampled:AlwaysOnSampler,localParentNotSampled:AlwaysOffSampler}"
	if got := sampler.Description(); got != want {
		t.Fatalf("default sampler = %s, want %s", got, want)
	}
}

// The queue's age is a histogram per transport, in seconds, and a message
// whose sender's clock ran ahead is counted at zero and not dropped.
func TestQueueRecordsTheAgeOfAMessageAtReceive(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	q, err := telemetry.NewQueue(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	if err != nil {
		t.Fatal(err)
	}
	q.Received("sqs", 12*time.Second)
	q.Received("sqs", -3*time.Second)

	var got metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &got); err != nil {
		t.Fatal(err)
	}
	var count uint64
	var sum float64
	for _, scope := range got.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "audit.queue.message.age" {
				continue
			}
			for _, p := range m.Data.(metricdata.Histogram[float64]).DataPoints {
				if v, _ := p.Attributes.Value(telemetry.AttrTransport); v.AsString() != "sqs" {
					t.Fatalf("transport = %q", v.AsString())
				}
				count += p.Count
				sum += p.Sum
			}
		}
	}
	if count != 2 || sum != 12 {
		t.Fatalf("count %d, sum %v; want 2 messages totalling 12s", count, sum)
	}
}

// An unregistered catalogue version is counted by the source and version the
// record names, and what an emitter can make up is bounded: past a few pairs the
// rest is "other", so one that sends anything cannot make a series each.
func TestUnknownCatalogueIsCountedByNameAndBoundedInLabels(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	w, err := telemetry.NewWriter(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	if err != nil {
		t.Fatal(err)
	}
	w.UnknownCatalogue("wallet", "2.0.0")
	w.UnknownCatalogue("wallet", "2.0.0")
	for i := 0; i < 100; i++ {
		w.UnknownCatalogue("made-up", fmt.Sprintf("%d.0.0", i))
	}
	var got metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &got); err != nil {
		t.Fatal(err)
	}
	counts, series := map[string]int64{}, 0
	for _, scope := range got.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "audit.writer.catalogue.unknown" {
				continue
			}
			for _, p := range m.Data.(metricdata.Sum[int64]).DataPoints {
				source, _ := p.Attributes.Value("source")
				version, _ := p.Attributes.Value("catalogue_version")
				counts[source.AsString()+"@"+version.AsString()] += p.Value
				series++
			}
		}
	}
	if counts["wallet@2.0.0"] != 2 {
		t.Errorf("counts = %v", counts)
	}
	if series > 21 || counts["other@other"] == 0 {
		t.Errorf("%d series, other = %d: the labels are not bounded", series, counts["other@other"])
	}
}
