// Package telemetry is the OpenTelemetry metrics and traces this repository's
// processes publish.
//
// It follows the fleet's shape: push over OTLP, and only when a collector is
// named. No process grows a listener for it. Where nothing names a collector,
// nothing is exported and every instrument records into a no-op, so metrics
// cost nothing where nobody collects them.
//
// Configuration is OpenTelemetry's own environment, read by its SDK:
// OTEL_EXPORTER_OTLP_ENDPOINT (or OTEL_EXPORTER_OTLP_METRICS_ENDPOINT),
// OTEL_EXPORTER_OTLP_TRACES_ENDPOINT, OTEL_EXPORTER_OTLP_HEADERS,
// OTEL_METRIC_EXPORT_INTERVAL, OTEL_TRACES_SAMPLER, OTEL_TRACES_SAMPLER_ARG,
// OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES. Nothing here restates it.
//
// Traces carry no personal data, and this package enforces that rather than
// hoping: see FilterExporter.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/truvity/audit/store"
)

// Enabled reports whether a collector is named in the environment for metrics.
func Enabled() bool {
	return named("OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT")
}

// TracesEnabled reports whether a collector is named for traces. It is the
// same rule as for metrics with the traces-specific variable: unset means
// nothing is exported and the tracer is the SDK's no-op, which costs nothing
// and cannot fail.
func TracesEnabled() bool {
	return named("OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
}

func named(vars ...string) bool {
	for _, name := range vars {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return true
		}
	}
	return false
}

// defaultSampler is the sampler used when OTEL_TRACES_SAMPLER names none: a
// parent-based always_on, which is the OpenTelemetry SDK's own default. Every
// new trace is kept and a caller's decision always wins. Thin the volume with
// OTEL_TRACES_SAMPLER (for example parentbased_traceidratio) and
// OTEL_TRACES_SAMPLER_ARG when the trace store needs it.
func defaultSampler() sdktrace.Sampler {
	return sdktrace.ParentBased(sdktrace.AlwaysSample())
}

// flushers are what ForceFlush calls: the providers Start installed.
var (
	flushMu  sync.Mutex
	flushers []func(context.Context) error
)

// Flush exports whatever the providers Start installed are holding, now. A
// process that is frozen between invocations (a function platform) cannot wait
// for the next periodic export, so it flushes before it returns. Without a
// collector there is nothing to flush.
func Flush(ctx context.Context) error {
	flushMu.Lock()
	fs := append([]func(context.Context) error(nil), flushers...)
	flushMu.Unlock()
	var errs []error
	for _, f := range fs {
		errs = append(errs, f(ctx))
	}
	return errors.Join(errs...)
}

// Start installs the global meter provider when a collector is named for
// metrics and the global tracer provider (and the W3C trace-context
// propagator) when one is named for traces, and returns what flushes and
// stops them. Without a collector it installs nothing and the returned
// function does nothing.
func Start(ctx context.Context, service, version string, log *slog.Logger) (func(context.Context) error, error) {
	if !Enabled() && !TracesEnabled() {
		return func(context.Context) error { return nil }, nil
	}
	attributes := []attribute.KeyValue{attribute.String("service.version", version)}
	if strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME")) == "" {
		attributes = append(attributes, attribute.String("service.name", service))
	}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(attributes...))
	if err != nil {
		return nil, fmt.Errorf("telemetry: the resource: %w", err)
	}
	var stops []func(context.Context) error
	flushMu.Lock()
	flushers = nil
	flushMu.Unlock()
	addFlusher := func(f func(context.Context) error) {
		flushMu.Lock()
		flushers = append(flushers, f)
		flushMu.Unlock()
	}
	if Enabled() {
		exporter, err := otlpmetrichttp.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("telemetry: an OTLP exporter: %w", err)
		}
		provider := sdkmetric.NewMeterProvider(
			sdkmetric.WithResource(res),
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)),
		)
		otel.SetMeterProvider(provider)
		stops = append(stops, provider.Shutdown)
		addFlusher(provider.ForceFlush)
		log.InfoContext(ctx, "publishing metrics over OTLP", "service", service)
	}
	if TracesEnabled() {
		exporter, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("telemetry: a trace exporter: %w", err)
		}
		opts := []sdktrace.TracerProviderOption{
			sdktrace.WithResource(res),
			sdktrace.WithBatcher(FilterExporter(exporter)),
		}
		if strings.TrimSpace(os.Getenv("OTEL_TRACES_SAMPLER")) == "" {
			opts = append(opts, sdktrace.WithSampler(defaultSampler()))
		} // else the SDK reads OTEL_TRACES_SAMPLER and OTEL_TRACES_SAMPLER_ARG itself.
		provider := sdktrace.NewTracerProvider(opts...)
		otel.SetTracerProvider(provider)
		otel.SetTextMapPropagator(propagation.TraceContext{})
		stops = append(stops, provider.Shutdown)
		addFlusher(provider.ForceFlush)
		log.InfoContext(ctx, "publishing traces over OTLP", "service", service)
	}
	return func(ctx context.Context) error {
		var errs []error
		for _, stop := range stops {
			errs = append(errs, stop(ctx))
		}
		return errors.Join(errs...)
	}, nil
}

// Writer is what the writer counts.
//
// Each instrument answers a question an operator would otherwise have to ask
// the logs. The writer does not index (observe does, and counts that in
// Observe); what is worth an alert here is a dead letter.
type Writer struct {
	objects, records, deadLettered, metaDropped, duplicatesLikely, notExtended metric.Int64Counter
}

// NewWriter makes the writer's instruments on the given provider, normally the
// global one Start installed.
func NewWriter(provider metric.MeterProvider) (*Writer, error) {
	m := provider.Meter("github.com/truvity/audit/writer")
	var w Writer
	for _, c := range []struct {
		into        *metric.Int64Counter
		name, unit  string
		description string
	}{
		{&w.objects, "audit.writer.objects.written", "{object}", "Objects put into the archive."},                  // audit:not-an-action — a metric name
		{&w.records, "audit.writer.records.written", "{record}", "Record copies in the objects put."},              // audit:not-an-action — a metric name
		{&w.deadLettered, "audit.writer.dead_lettered", "{record}", "Records the writer could not process."},       // audit:not-an-action — a metric name
		{&w.metaDropped, "audit.writer.meta.dropped", "{record}", "The writer's own records it could not record."}, // audit:not-an-action — a metric name
		{&w.duplicatesLikely, "audit.writer.duplicates.likely", "{record}", // audit:not-an-action — a metric name
			"Records written but not marked as written, so a redelivery will be written again."},
		{&w.notExtended, "audit.writer.retention.not_extended", "{object}", // audit:not-an-action — a metric name
			"Objects an addendum should have locked for longer and did not."},
	} {
		counter, err := m.Int64Counter(c.name, metric.WithUnit(c.unit), metric.WithDescription(c.description))
		if err != nil {
			return nil, fmt.Errorf("telemetry: %s: %w", c.name, err)
		}
		*c.into = counter
	}
	return &w, nil
}

// Written counts one object put.
func (w *Writer) Written(key string, records int) {
	at := metric.WithAttributes(attribute.String("profile", ProfileOf(key)))
	w.objects.Add(context.Background(), 1, at)
	w.records.Add(context.Background(), int64(records), at)
}

// DeadLettered counts one record the writer could not process.
func (w *Writer) DeadLettered() { w.deadLettered.Add(context.Background(), 1) }

// MetaDropped counts one of the writer's own records that was lost.
func (w *Writer) MetaDropped() { w.metaDropped.Add(context.Background(), 1) }

// DuplicatesLikely counts records a redelivery would write again.
func (w *Writer) DuplicatesLikely(n int) { w.duplicatesLikely.Add(context.Background(), int64(n)) }

// RetentionNotExtended counts one object an addendum could not lengthen the
// lock of. The addendum itself is written; what is at risk is the earlier
// evidence, which keeps its old date until somebody extends it by hand.
func (w *Writer) RetentionNotExtended(profile string) {
	w.notExtended.Add(context.Background(), 1, metric.WithAttributes(attribute.String("profile", profile)))
}

// ProfileOf is the profile an archive key is under, or "" for a key outside
// the records layout (records/<profile>/...). It is the one label these
// counters carry: a profile is a handful of names a deployment chose, where a
// tenant would be thousands.
func ProfileOf(key string) string {
	rest, ok := strings.CutPrefix(key, store.RecordsPrefix)
	if !ok {
		return ""
	}
	profile, _, found := strings.Cut(rest, "/")
	if !found {
		return ""
	}
	return profile
}

// Observe is what the indexer counts.
//
// The one to alert on is Deferred: the archive is fine when it rises, but the
// index a reader searches is behind it, and nobody notices an index that is
// quietly behind until it answers wrongly.
type Observe struct {
	objects, records, deferred metric.Int64Counter
	indexLag                   metric.Float64Histogram
}

// NewObserve makes the indexer's instruments on the given provider.
func NewObserve(provider metric.MeterProvider) (*Observe, error) {
	m := provider.Meter("github.com/truvity/audit/observe")
	var o Observe
	for _, c := range []struct {
		into        *metric.Int64Counter
		name, unit  string
		description string
	}{
		{&o.objects, "audit.observe.objects.indexed", "{object}", "Objects whose rows are in the index."},  // audit:not-an-action — a metric name
		{&o.records, "audit.observe.records.indexed", "{record}", "Record copies in the objects indexed."}, // audit:not-an-action — a metric name
		{&o.deferred, "audit.observe.index.deferred", "{object}", // audit:not-an-action — a metric name
			"Objects the indexer could not index: reason=unreadable were skipped, for they will not read later either; " +
				"reason=retry will be tried again by the next pass."},
	} {
		counter, err := m.Int64Counter(c.name, metric.WithUnit(c.unit), metric.WithDescription(c.description))
		if err != nil {
			return nil, fmt.Errorf("telemetry: %s: %w", c.name, err)
		}
		*c.into = counter
	}
	lag, err := m.Float64Histogram("audit.observe.index.lag", metric.WithUnit("s"), // audit:not-an-action — a metric name
		metric.WithDescription("Seconds from an object's put into the archive to its rows being in the index, per profile. "+
			"The settle window is its floor: the indexer does not look at an object younger than that."),
		metric.WithExplicitBucketBoundaries(5, 15, 30, 60, 120, 180, 300, 600, 1800, 3600))
	if err != nil {
		return nil, fmt.Errorf("telemetry: audit.observe.index.lag: %w", err)
	}
	o.indexLag = lag
	return &o, nil
}

// Indexed counts one object whose rows are in the index, and records how long
// after the object reached the archive that was. A profile is a handful of
// names, so it is the only label; a tenant would be thousands.
func (o *Observe) Indexed(profile string, rows int, lag time.Duration) {
	at := metric.WithAttributes(attribute.String("profile", profile))
	o.objects.Add(context.Background(), 1, at)
	o.records.Add(context.Background(), int64(rows), at)
	o.indexLag.Record(context.Background(), lag.Seconds(), at)
}

// Deferred counts one object the indexer did not take. Permanent is an object
// that will never read and has been skipped; otherwise it is retried.
func (o *Observe) Deferred(profile string, permanent bool) {
	reason := "retry"
	if permanent {
		reason = "unreadable"
	}
	o.deferred.Add(context.Background(), 1,
		metric.WithAttributes(attribute.String("profile", profile), attribute.String("reason", reason)))
}
