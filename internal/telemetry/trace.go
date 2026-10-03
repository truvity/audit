package telemetry

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// Span attribute keys this repository sets itself.
const (
	AttrTransport  = "audit.transport"  // audit:not-an-action — a span attribute name
	AttrDelivery   = "audit.delivery"   // audit:not-an-action — a span attribute name
	AttrDurability = "audit.durability" // audit:not-an-action — a span attribute name
	AttrOutcome    = "audit.outcome"    // audit:not-an-action — a span attribute name
	AttrRecords    = "audit.records"    // audit:not-an-action — a span attribute name
	AttrRejected   = "audit.rejected"   // audit:not-an-action — a span attribute name
	AttrTenant     = "audit.tenant.id"  // audit:not-an-action — a span attribute name
	AttrAction     = "audit.action"     // audit:not-an-action — a span attribute name
)

// SpanAttributeAllowlist is every attribute a span leaving this process may
// carry. The first group is ours: an action, an outcome, a tenant id, a
// durability, a count, a transport. The second is the shape of the RPC or HTTP
// request, which names a method and a status and nothing about the caller.
//
// Everything else is dropped on the way out by FilterExporter, whoever set it:
// the HTTP and RPC instrumentation record the peer's address, a recorded error
// carries its message, and a message may quote a record. Spans are read
// unscoped by everyone with any grant on the trace store, so the rule that
// they hold no personal data has to be a property of the exporter, not a
// promise by each caller. Never an actor, a subject, a client address or
// record data.
var SpanAttributeAllowlist = map[attribute.Key]bool{
	AttrTransport: true, AttrDelivery: true, AttrDurability: true, AttrOutcome: true,
	AttrRecords: true, AttrRejected: true, AttrTenant: true, AttrAction: true,

	"rpc.system": true, "rpc.service": true, "rpc.method": true,
	"rpc.connect_rpc.error_code": true,
	"http.request.method":        true, "http.response.status_code": true, "http.route": true,
	"url.path": true,
}

// FilterExporter wraps an exporter so that only SpanAttributeAllowlist leaves
// the process. Span events (a recorded error's message is one), links'
// attributes and a status description are removed outright: they are free
// text.
func FilterExporter(next sdktrace.SpanExporter) sdktrace.SpanExporter {
	return filtering{next}
}

type filtering struct{ next sdktrace.SpanExporter }

func (f filtering) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	out := make([]sdktrace.ReadOnlySpan, 0, len(spans))
	stubs := make(tracetest.SpanStubs, 0, len(spans))
	for _, s := range spans {
		stub := tracetest.SpanStubFromReadOnlySpan(s)
		stub.Attributes = keep(stub.Attributes)
		stub.Events = nil
		stub.Status.Description = ""
		for i := range stub.Links {
			stub.Links[i].Attributes = nil
		}
		stubs = append(stubs, stub)
	}
	out = append(out, stubs.Snapshots()...)
	return f.next.ExportSpans(ctx, out)
}

func (f filtering) Shutdown(ctx context.Context) error { return f.next.Shutdown(ctx) }

func keep(in []attribute.KeyValue) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(in))
	for _, kv := range in {
		if SpanAttributeAllowlist[kv.Key] {
			out = append(out, kv)
		}
	}
	return out
}

// ConnectOptions instruments a Connect handler: one server span per call; the
// interceptor adds no peer attributes unless asked, and FilterExporter would
// drop them regardless. Without a tracer provider installed it records nothing.
func ConnectOptions() []connect.HandlerOption {
	i, err := otelconnect.NewInterceptor(
		// This repository's own instruments carry the metrics; the
		// interceptor's would double them under another name.
		otelconnect.WithoutMetrics(),
	)
	if err != nil {
		// NewInterceptor fails only on a bad option, and there are none.
		panic("telemetry: " + err.Error())
	}
	return []connect.HandlerOption{connect.WithInterceptors(i)}
}

// ConnectClientOptions is the same for a client.
func ConnectClientOptions() []connect.ClientOption {
	i, err := otelconnect.NewInterceptor(otelconnect.WithoutMetrics())
	if err != nil {
		panic("telemetry: " + err.Error())
	}
	return []connect.ClientOption{connect.WithInterceptors(i)}
}

// HTTPHandler wraps a server's mux in an HTTP server span, continuing the
// caller's traceparent. The liveness probe is left out: a span every few
// seconds per pod is noise.
func HTTPHandler(h http.Handler, service string) http.Handler {
	return otelhttp.NewHandler(h, service,
		otelhttp.WithFilter(func(r *http.Request) bool { return r.URL.Path != "/healthz" }),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Method + " " + r.URL.Path }),
	)
}

// HTTPClient returns a client that propagates the trace to what it calls.
func HTTPClient(base http.RoundTripper) *http.Client {
	return &http.Client{Transport: otelhttp.NewTransport(base)}
}
