package sink_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/truvity/audit/internal/telemetry"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

func install(t *testing.T) (*tracetest.SpanRecorder, *sdkmetric.ManualReader) {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	reader := sdkmetric.NewManualReader()
	oldT, oldM := otel.GetTracerProvider(), otel.GetMeterProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans), sdktrace.WithSampler(sdktrace.AlwaysSample())))
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { otel.SetTracerProvider(oldT); otel.SetMeterProvider(oldM) })
	return spans, reader
}

// A span carries an action, an outcome, a tenant id, a durability, counts and a
// transport, and nothing that names a person: the actor, the subject, the
// client's address, and the data of the record never reach an attribute. The
// test sets every one of them to a marker and looks for it everywhere.
func TestSpansCarryOnlyAllowlistedAttributes(t *testing.T) {
	spans, _ := install(t)
	rec := &record.Record{
		Action: "shop.order.placed", TenantId: "acme",
		Actor: &record.Actor{Kind: "user", Id: "MARKER-actor"},
	}
	req := &sink.Request{Records: []*record.Record{rec, rec}, Delivery: sink.Block}

	for transport, kind := range map[string]trace.SpanKind{
		sink.TransportNATS: trace.SpanKindProducer, sink.TransportSQS: trace.SpanKindProducer,
		sink.TransportConnectClient: trace.SpanKindClient, sink.TransportConnectServer: trace.SpanKindInternal,
	} {
		_, done := sink.Observe(context.Background(), transport, kind, req)
		done(&sink.Result{Accepted: 1, Durability: sink.Queued, Rejected: []sink.Rejection{{ID: "x", Reason: "MARKER-reason"}}}, nil)
		_, done = sink.Observe(context.Background(), transport, kind, req)
		done(nil, errors.New("MARKER-error quoting a record"))
	}

	ended := spans.Ended()
	if len(ended) != 8 {
		t.Fatalf("%d spans, want 8", len(ended))
	}
	seen := map[attribute.Key]bool{}
	for _, s := range ended {
		for _, kv := range s.Attributes() {
			seen[kv.Key] = true
			if !telemetry.SpanAttributeAllowlist[kv.Key] {
				t.Errorf("span %q carries %s, which is not on the allowlist", s.Name(), kv.Key)
			}
			if strings.Contains(kv.Value.String(), "MARKER") {
				t.Errorf("span %q: %s holds personal data: %s", s.Name(), kv.Key, kv.Value.String())
			}
		}
		if len(s.Events()) != 0 || strings.Contains(s.Status().Description, "MARKER") {
			t.Errorf("span %q records an event or a status text", s.Name())
		}
	}
	// The allowed ones that are set must actually be set, or this proves nothing.
	for _, k := range []attribute.Key{telemetry.AttrTransport, telemetry.AttrDelivery, telemetry.AttrRecords,
		telemetry.AttrTenant, telemetry.AttrAction, telemetry.AttrOutcome, telemetry.AttrDurability, telemetry.AttrRejected} {
		if !seen[k] {
			t.Errorf("no span carries %s", k)
		}
	}
}

func TestAWriteIsCountedByDurabilityAndTransport(t *testing.T) {
	_, reader := install(t)
	req := &sink.Request{Records: sinkRecords(3), Delivery: sink.Async}
	_, done := sink.Observe(context.Background(), sink.TransportNATS, trace.SpanKindProducer, req)
	done(&sink.Result{Accepted: 2, Durability: sink.Queued, Rejected: []sink.Rejection{{ID: "x"}}}, nil)
	_, done = sink.Observe(context.Background(), sink.TransportConnectServer, trace.SpanKindInternal, req)
	done(&sink.Result{Accepted: 3, Durability: sink.Archived}, nil)
	sink.ConsumeFailed(context.Background(), sink.TransportSQS)

	var got metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &got); err != nil {
		t.Fatal(err)
	}
	acked := map[string]int64{}
	var rejected, failures, writes int64
	for _, sc := range got.ScopeMetrics {
		for _, m := range sc.Metrics {
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, p := range d.DataPoints {
					tr, _ := p.Attributes.Value("transport")
					du, _ := p.Attributes.Value("durability")
					switch m.Name {
					case "audit.sink.records.acknowledged":
						acked[tr.AsString()+"/"+du.AsString()] += p.Value
					case "audit.sink.records.rejected":
						rejected += p.Value
					case "audit.sink.consume.failures":
						failures += p.Value
					}
				}
			case metricdata.Histogram[float64]:
				if m.Name == "audit.sink.write.duration" {
					for _, p := range d.DataPoints {
						writes += int64(p.Count)
					}
				}
			}
		}
	}
	if acked["nats/queued"] != 2 || acked["connect-server/archived"] != 3 || rejected != 1 || failures != 1 || writes != 2 {
		t.Fatalf("acked %v rejected %d failures %d writes %d", acked, rejected, failures, writes)
	}
}

func sinkRecords(n int) []*record.Record {
	out := make([]*record.Record, n)
	for i := range out {
		out[i] = &record.Record{Action: "a.b.c", TenantId: "t"}
	}
	return out
}
