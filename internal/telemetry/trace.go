package telemetry

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	sdktelemetry "github.com/truvity/audit/sdk/telemetry"
)

// The span attribute names, the allowlist and the Connect interceptors are the
// SDK's, because an emitter needs them too; they are re-exported here so that
// the services keep one place to look.
const (
	AttrTransport  = sdktelemetry.AttrTransport
	AttrDelivery   = sdktelemetry.AttrDelivery
	AttrDurability = sdktelemetry.AttrDurability
	AttrOutcome    = sdktelemetry.AttrOutcome
	AttrRecords    = sdktelemetry.AttrRecords
	AttrRejected   = sdktelemetry.AttrRejected
	AttrTenant     = sdktelemetry.AttrTenant
	AttrAction     = sdktelemetry.AttrAction
)

// SpanAttributeAllowlist is every attribute a span leaving this process may
// carry; see the SDK's telemetry package for what it holds and why.
var SpanAttributeAllowlist = sdktelemetry.SpanAttributeAllowlist

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

// ConnectOptions instruments a Connect handler: one server span per call.
func ConnectOptions() []connect.HandlerOption { return sdktelemetry.ConnectOptions() }

// ConnectClientOptions is the same for a client.
func ConnectClientOptions() []connect.ClientOption { return sdktelemetry.ConnectClientOptions() }

// HTTPHandler wraps a server's mux in an HTTP server span, continuing the
// caller's traceparent. The liveness and readiness probes are left out: a span every few
// seconds per pod is noise.
func HTTPHandler(h http.Handler, service string) http.Handler {
	return otelhttp.NewHandler(h, service,
		otelhttp.WithFilter(func(r *http.Request) bool { return r.URL.Path != "/healthz" && r.URL.Path != "/readyz" }),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Method + " " + r.URL.Path }),
	)
}

// HTTPClient returns a client that propagates the trace to what it calls.
func HTTPClient(base http.RoundTripper) *http.Client {
	return &http.Client{Transport: otelhttp.NewTransport(base)}
}
