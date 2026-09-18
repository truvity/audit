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
// best_effort delivery is the one that needs it most: the design's answer to
// "may drop under pressure" is "and alerts", and until something counts the
// drops nothing can alert. So the instruments are:
//
//	audit.emit.records.written   records a sink accepted, by delivery
//	audit.emit.records.dropped   records best-effort delivery gave up — alert on this
//	audit.emit.records.refused   records that do not satisfy their catalogue: a bug in the emitting code
//	audit.emit.batches.failed    batches a sink refused or failed, by delivery
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
		metric.WithDescription("Records best-effort delivery gave up."))
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

// InstrumentOutbox publishes how many records an outbox holds as
// audit.emit.outbox.pending. A number that only grows is a sink that has been
// gone too long — the delay an outbox is for, turning into a backlog.
func InstrumentOutbox(box Outbox, provider metric.MeterProvider) error {
	m := provider.Meter("github.com/truvity/audit/emit")
	_, err := m.Int64ObservableGauge("audit.emit.outbox.pending", // audit:not-an-action — a metric name
		metric.WithUnit("{record}"),
		metric.WithDescription("Records waiting in the outbox."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			n, err := box.Pending()
			if err != nil {
				return err
			}
			o.Observe(int64(n))
			return nil
		}))
	if err != nil {
		return fmt.Errorf("emit: %w", err)
	}
	return nil
}
