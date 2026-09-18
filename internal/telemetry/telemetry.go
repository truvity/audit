// Package telemetry is the OpenTelemetry metrics this repository's processes
// publish.
//
// It follows the fleet's shape: push over OTLP, and only when a collector is
// named. No process grows a listener for it. Where nothing names a collector,
// nothing is exported and every instrument records into a no-op, so metrics
// cost nothing where nobody collects them.
//
// Configuration is OpenTelemetry's own environment, read by its SDK:
// OTEL_EXPORTER_OTLP_ENDPOINT (or OTEL_EXPORTER_OTLP_METRICS_ENDPOINT),
// OTEL_EXPORTER_OTLP_HEADERS, OTEL_METRIC_EXPORT_INTERVAL,
// OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES. Nothing here restates it.
package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

// Enabled reports whether a collector is named in the environment.
func Enabled() bool {
	for _, name := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return true
		}
	}
	return false
}

// Start installs the global meter provider when a collector is named, and
// returns what flushes and stops it. Without one it installs nothing and the
// returned function does nothing.
func Start(ctx context.Context, service, version string, log *slog.Logger) (func(context.Context) error, error) {
	if !Enabled() {
		return func(context.Context) error { return nil }, nil
	}
	exporter, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("telemetry: an OTLP exporter: %w", err)
	}
	attributes := []attribute.KeyValue{attribute.String("service.version", version)}
	if strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME")) == "" {
		attributes = append(attributes, attribute.String("service.name", service))
	}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(attributes...))
	if err != nil {
		return nil, fmt.Errorf("telemetry: the resource: %w", err)
	}
	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)),
	)
	otel.SetMeterProvider(provider)
	log.InfoContext(ctx, "publishing metrics over OTLP", "service", service)
	return provider.Shutdown, nil
}

// Writer is what the writer counts.
//
// Each instrument answers a question an operator would otherwise have to ask
// the logs. The one worth an alert is IndexDeferred: the archive is fine when
// it rises, but the index a reader searches is behind it, and nobody notices an
// index that is quietly behind until it answers wrongly.
type Writer struct {
	objects, records, deferred, deadLettered, metaDropped, duplicatesLikely metric.Int64Counter
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
