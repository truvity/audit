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
// the logs. The one worth an alert is IndexDeferred: the archive is fine when
// it rises, but the index a reader searches is behind it, and nobody notices an
// index that is quietly behind until it answers wrongly.
type Writer struct {
	objects, records, deferred, deadLettered, metaDropped, duplicatesLikely, notExtended metric.Int64Counter
	indexLag                                                                             metric.Float64Histogram
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
		{&w.objects, "audit.writer.objects.written", "{object}", "Objects put into the archive."},     // audit:not-an-action — a metric name
		{&w.records, "audit.writer.records.written", "{record}", "Record copies in the objects put."}, // audit:not-an-action — a metric name
		{&w.deferred, "audit.writer.index.deferred", "{row}", // audit:not-an-action — a metric name
			"Rows of objects written to the archive but not to the index; repaired by audit reindex."},
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
	lag, err := m.Float64Histogram("audit.writer.index.lag", metric.WithUnit("s"), // audit:not-an-action — a metric name
		metric.WithDescription("Seconds from an object's put into the archive to its rows being in the index, per profile. "+
			"Rows that never reach the index are in audit.writer.index.deferred instead."),
		metric.WithExplicitBucketBoundaries(0.005, 0.025, 0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 300))
	if err != nil {
		return nil, fmt.Errorf("telemetry: audit.writer.index.lag: %w", err)
	}
	w.indexLag = lag
	return &w, nil
}

// IndexLag records how long the rows of one object took to reach the index
// after the object was in the archive. A profile is a handful of names, so it
// is the only label; a tenant would be thousands.
func (w *Writer) IndexLag(key string, d time.Duration) {
	w.indexLag.Record(context.Background(), d.Seconds(), metric.WithAttributes(attribute.String("profile", ProfileOf(key))))
}

// Written counts one object put.
func (w *Writer) Written(key string, records int) {
	at := metric.WithAttributes(attribute.String("profile", ProfileOf(key)))
	w.objects.Add(context.Background(), 1, at)
	w.records.Add(context.Background(), int64(records), at)
}

// IndexDeferred counts the rows of one object the index did not take.
func (w *Writer) IndexDeferred(key string, rows int) {
	w.deferred.Add(context.Background(), int64(rows), metric.WithAttributes(attribute.String("profile", ProfileOf(key))))
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

// DigestAge publishes how old the newest sealed digest window is, per profile,
// as audit.digest.age in seconds: the time since the window's end. The hourly
// job seals the hour that has just closed, so a healthy value runs from zero to
// a little over an hour; a value that keeps climbing is a chain that has
// stopped growing, which nothing else shows until somebody verifies.
//
// It is observed in the writer, which runs all day, and not pushed by the job:
// a job that exits in seconds is attributed to the previous occupant of its
// address by the gateway and its series goes stale a few minutes later.
//
// newest says where a profile's newest digest window ended. The answer is
// cached for refresh (the age is computed afresh from the cached window end), because the callback runs on every collection and what
// it asks is a request to the archive. An error keeps the last answer. A
// profile with no digest within the lookback reports the lookback, which is
// "at least this old".
func DigestAge(provider metric.MeterProvider, profiles []string, lookback, refresh time.Duration,
	newest func(ctx context.Context, profile string) (end time.Time, ok bool, err error), now func() time.Time,
) error {
	if now == nil {
		now = time.Now
	}
	var (
		mu   sync.Mutex
		at   time.Time
		ends = map[string]time.Time{} // zero: none within the lookback
	)
	m := provider.Meter("github.com/truvity/audit/digest")
	_, err := m.Float64ObservableGauge("audit.digest.age", metric.WithUnit("s"), // audit:not-an-action — a metric name
		metric.WithDescription("Seconds since the end of the newest sealed digest window, per profile."),
		metric.WithFloat64Callback(func(ctx context.Context, o metric.Float64Observer) error {
			mu.Lock()
			defer mu.Unlock()
			if t := now(); at.IsZero() || t.Sub(at) >= refresh {
				at = t
				probe, cancel := context.WithTimeout(ctx, 20*time.Second)
				defer cancel()
				for _, p := range profiles {
					end, ok, err := newest(probe, p)
					switch {
					case err != nil:
						// Keep what was known: an archive that could not
						// answer is not evidence the chain stopped.
					case !ok:
						ends[p] = time.Time{}
					default:
						ends[p] = end
					}
				}
			}
			for p, end := range ends {
				v := lookback.Seconds()
				if !end.IsZero() {
					v = max(0, now().Sub(end).Seconds())
				}
				o.Observe(v, metric.WithAttributes(attribute.String("profile", p)))
			}
			return nil
		}))
	if err != nil {
		return fmt.Errorf("telemetry: audit.digest.age: %w", err)
	}
	return nil
}

// ProfileOf is the profile an archive key is under, or "" for a key outside
// the profile layout. It is the one label these counters carry: a profile is
// a handful of names a deployment chose, where a tenant would be thousands.
func ProfileOf(key string) string {
	for _, segment := range strings.Split(key, "/") {
		if name, ok := strings.CutPrefix(segment, "profile="); ok {
			return name
		}
	}
	return ""
}
