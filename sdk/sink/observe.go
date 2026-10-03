package sink

import (
	"context"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/truvity/audit/sdk/record"
	"github.com/truvity/audit/sdk/telemetry"
)

// The transports a write can cross, which are the one label the sink
// instruments carry besides durability. Four values, so it is safe as a label;
// a tenant would be thousands and is a span attribute only.
const (
	TransportConnectClient = "connect-client"
	TransportConnectServer = "connect-server"
	TransportNATS          = "nats"
	TransportSQS           = "sqs"
)

const instrumentation = "github.com/truvity/audit/sdk/sink"

// Observe starts the span of one write across a transport and returns the
// context to pass on, and the function that ends it with the outcome. It
// counts, once the write is over:
//
//	audit.sink.records.acknowledged  records the hop took, by durability and transport
//	audit.sink.records.rejected      records the hop refused, by transport
//	audit.sink.write.duration        seconds a write took, by transport and outcome
//
// A span carries only what telemetry.SpanAttributeAllowlist names: transport,
// delivery, a count, the tenant and action when the whole batch shares one, and
// at the end the durability and outcome. The tracer and meter are the global
// ones, which are no-ops unless telemetry.Start installed a provider.
func Observe(ctx context.Context, transport string, kind trace.SpanKind, req *Request) (context.Context, func(*Result, error)) {
	start := time.Now()
	attrs := []attribute.KeyValue{
		attribute.String(telemetry.AttrTransport, transport),
		attribute.String(telemetry.AttrDelivery, deliveryName(req.Delivery)),
		attribute.Int(telemetry.AttrRecords, len(req.Records)),
	}
	if t := sameOf(req, (*record.Record).GetTenantId); t != "" {
		attrs = append(attrs, attribute.String(telemetry.AttrTenant, t))
	}
	if a := sameOf(req, (*record.Record).GetAction); a != "" {
		attrs = append(attrs, attribute.String(telemetry.AttrAction, a))
	}
	ctx, span := otel.Tracer(instrumentation).Start(ctx, "audit.sink.write "+transport,
		trace.WithSpanKind(kind), trace.WithAttributes(attrs...))

	return ctx, func(res *Result, err error) {
		outcome := "ok"
		switch {
		case err != nil:
			outcome = "error"
		case res != nil && len(res.Rejected) > 0:
			outcome = "rejected"
		}
		span.SetAttributes(attribute.String(telemetry.AttrOutcome, outcome))
		if res != nil {
			span.SetAttributes(
				attribute.String(telemetry.AttrDurability, durabilityName(res.Durability)),
				attribute.Int(telemetry.AttrRejected, len(res.Rejected)))
		}
		if err != nil {
			// No description: an error's text may quote a record.
			span.SetStatus(codes.Error, "")
		}
		span.End()

		m := otel.Meter(instrumentation)
		on := metric.WithAttributes(attribute.String("transport", transport))
		if h, e := m.Float64Histogram("audit.sink.write.duration", metric.WithUnit("s"), // audit:not-an-action — a metric name
			metric.WithDescription("Seconds one write took at this hop, by transport and outcome."),
			metric.WithExplicitBucketBoundaries(0.005, 0.025, 0.1, 0.25, 0.5, 1, 2.5, 5, 10)); e == nil {
			h.Record(ctx, time.Since(start).Seconds(), on, metric.WithAttributes(attribute.String("outcome", outcome)))
		}
		if res == nil {
			return
		}
		if c, e := m.Int64Counter("audit.sink.records.acknowledged", metric.WithUnit("{record}"), // audit:not-an-action — a metric name
			metric.WithDescription("Records a hop acknowledged, by the durability it promised and the transport.")); e == nil && res.Accepted > 0 {
			c.Add(ctx, int64(res.Accepted), on, metric.WithAttributes(attribute.String("durability", durabilityName(res.Durability))))
		}
		if c, e := m.Int64Counter("audit.sink.records.rejected", metric.WithUnit("{record}"), // audit:not-an-action — a metric name
			metric.WithDescription("Records a hop refused for the record's own sake, by transport.")); e == nil && len(res.Rejected) > 0 {
			c.Add(ctx, int64(len(res.Rejected)), on)
		}
	}
}

// ConsumeFailed counts one batch a queue consumer's target refused or failed,
// which the queue will deliver again. It is the number to alert on for a
// consumer that is going round in circles.
func ConsumeFailed(ctx context.Context, transport string) {
	if c, err := otel.Meter(instrumentation).Int64Counter("audit.sink.consume.failures", metric.WithUnit("{batch}"), // audit:not-an-action — a metric name
		metric.WithDescription("Batches a queue consumer's target refused or failed, to be delivered again, by transport.")); err == nil {
		c.Add(ctx, 1, metric.WithAttributes(attribute.String("transport", transport)))
	}
}

func deliveryName(d Delivery) string {
	return strings.ToLower(strings.TrimPrefix(d.String(), "DELIVERY_"))
}

// sameOf is the value every record of the batch shares, or "" when they differ
// or there are none.
func sameOf(req *Request, f func(*record.Record) string) string {
	var first string
	for i, r := range req.Records {
		v := f(r)
		if i == 0 {
			first = v
		} else if v != first {
			return ""
		}
	}
	return first
}
