package emit

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

// Instrument returns hooks that count what an emitter does into OpenTelemetry
// metrics, and then call the given hooks.
//
// Async delivery is the one that needs it most: it gives a record up only when
// its queue overflows, and until something counts that nothing can alert on
// it. So the instruments are:
//
//	audit.emit.records.written   records a sink accepted, by delivery
//	audit.emit.records.dropped   records the queue gave up — alert on this
//	audit.emit.records.refused   records that do not satisfy their catalogue: a bug in the emitting code
//	audit.emit.batches.failed    batches a sink refused or could not take, by delivery
//
// With InstrumentQueue below, audit.emit.queue.pending is the one to watch:
// drops are what happens after it has been climbing.
//
// The emitter itself stays free of OpenTelemetry: this wraps its hooks, so an
// application that wants neither pays for neither.
func Instrument(h Hooks, provider metric.MeterProvider) (Hooks, error) {
	m := provider.Meter("github.com/truvity/audit/emit")
	written, err := m.Int64Counter("audit.emit.records.written", metric.WithUnit("{record}"), // audit:not-an-action — a metric name
		metric.WithDescription("Records a sink accepted."))
	if err != nil {
		return h, fmt.Errorf("emit: %w", err)
	}
	dropped, err := m.Int64Counter("audit.emit.records.dropped", metric.WithUnit("{record}"), // audit:not-an-action — a metric name
		metric.WithDescription("Records the async queue gave up."))
	if err != nil {
		return h, fmt.Errorf("emit: %w", err)
	}
	refused, err := m.Int64Counter("audit.emit.records.refused", metric.WithUnit("{record}"), // audit:not-an-action — a metric name
		metric.WithDescription("Records that do not satisfy their catalogue."))
	if err != nil {
		return h, fmt.Errorf("emit: %w", err)
	}
	failed, err := m.Int64Counter("audit.emit.batches.failed", metric.WithUnit("{batch}"), // audit:not-an-action — a metric name
		metric.WithDescription("Batches a sink refused or failed."))
	if err != nil {
		return h, fmt.Errorf("emit: %w", err)
	}

	inner := h
	return Hooks{
		OnWritten: func(n int, delivery sink.Delivery) {
			written.Add(context.Background(), int64(n), metric.WithAttributes(attribute.String("delivery", delivery.String())))
			if inner.OnWritten != nil {
				inner.OnWritten(n, delivery)
			}
		},
		OnDropped: func(r *record.Record, reason string) {
			dropped.Add(context.Background(), 1, metric.WithAttributes(attribute.String("action", r.GetAction())))
			if inner.OnDropped != nil {
				inner.OnDropped(r, reason)
			}
		},
		OnRefused: func(r *record.Record, err error) {
			refused.Add(context.Background(), 1, metric.WithAttributes(attribute.String("action", r.GetAction())))
			if inner.OnRefused != nil {
				inner.OnRefused(r, err)
			}
		},
		OnFailed: func(err error, delivery sink.Delivery, n int) {
			failed.Add(context.Background(), 1, metric.WithAttributes(attribute.String("delivery", delivery.String())))
			if inner.OnFailed != nil {
				inner.OnFailed(err, delivery, n)
			}
		},
	}, nil
}

// InstrumentQueue publishes how many records are waiting to be acknowledged as
// audit.emit.queue.pending. It is the number that says how much this process
// would lose if it died now, and a number that only climbs is a sink that has
// been away longer than the queue is deep — drops follow.
func InstrumentQueue(e *Emitter, provider metric.MeterProvider) error {
	m := provider.Meter("github.com/truvity/audit/emit")
	_, err := m.Int64ObservableGauge("audit.emit.queue.pending", // audit:not-an-action — a metric name
		metric.WithUnit("{record}"),
		metric.WithDescription("Records queued for async delivery and not yet acknowledged."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(e.Pending()))
			return nil
		}))
	if err != nil {
		return fmt.Errorf("emit: %w", err)
	}
	return nil
}
