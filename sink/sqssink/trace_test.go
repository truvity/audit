package sqssink_test

import (
	"context"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/truvity/audit/sdk/sink"
	"github.com/truvity/audit/sdk/sink/sinktest"
)

// The trace crosses the queue: what the publisher is called under is the
// parent of what the consumer does with the message, and the context is on the
// wire as a `traceparent` message attribute.
func TestTheTraceCrossesTheQueue(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	oldTP, oldProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTracerProvider(oldTP); otel.SetTextMapPropagator(oldProp) })

	ctx, root := tp.Tracer("test").Start(context.Background(), "request")
	f := newFake()
	if _, err := publisher(t, f, standard).Write(ctx, &sink.Request{Records: sinktest.Records(2)}); err != nil {
		t.Fatal(err)
	}
	root.End()

	if tp := f.sent[0].Entries[0].MessageAttributes["traceparent"]; tp.StringValue == nil || *tp.StringValue == "" {
		t.Fatalf("no traceparent on the message: %+v", f.sent[0].Entries[0].MessageAttributes)
	}

	var (
		mu   sync.Mutex
		seen []trace.SpanContext
	)
	stop := run(t, f, sink.Func(func(ctx context.Context, req *sink.Request) (*sink.Result, error) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, trace.SpanContextFromContext(ctx))
		return &sink.Result{Accepted: len(req.Records), Durability: sink.Archived}, nil
	}), nil)
	eventually(t, "the consumer to write", func() bool { mu.Lock(); defer mu.Unlock(); return len(seen) > 0 })
	stop()

	if got, want := seen[0].TraceID(), root.SpanContext().TraceID(); got != want {
		t.Fatalf("the consumer is in trace %s, the publisher in %s", got, want)
	}
}

// Without a tracer provider nothing is added to the message and nothing fails.
func TestNoTracerNoTraceparent(t *testing.T) {
	otel.SetTracerProvider(noop.NewTracerProvider())
	otel.SetTextMapPropagator(propagation.TraceContext{})
	f := newFake()
	if _, err := publisher(t, f, standard).Write(context.Background(), &sink.Request{Records: sinktest.Records(1)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.sent[0].Entries[0].MessageAttributes["traceparent"]; ok {
		t.Fatal("a traceparent was written with no trace in flight")
	}
}
