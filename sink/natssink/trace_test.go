package natssink_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/truvity/audit/sdk/record"
	"github.com/truvity/audit/sdk/sink"
	"github.com/truvity/audit/sink/natssink"
)

// The trace crosses the stream: the publisher puts `traceparent` in the
// message's headers and the consumer's write is in the same trace.
func TestTheTraceCrossesTheStream(t *testing.T) {
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(tracetest.NewSpanRecorder()), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	oldTP, oldProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTracerProvider(oldTP); otel.SetTextMapPropagator(oldProp) })

	js, s, _ := stream(t)
	p, err := natssink.NewPublisher(js, natssink.Options{Subject: subject})
	if err != nil {
		t.Fatal(err)
	}
	ctx, root := tp.Tracer("test").Start(context.Background(), "request")
	if _, err := p.Write(ctx, &sink.Request{Records: []*record.Record{one(t)}, Delivery: sink.Block}); err != nil {
		t.Fatal(err)
	}
	root.End()

	var (
		mu   sync.Mutex
		seen []trace.SpanContext
	)
	target := sink.Func(func(ctx context.Context, req *sink.Request) (*sink.Result, error) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, trace.SpanContextFromContext(ctx))
		return &sink.Result{Accepted: len(req.Records), Durability: sink.Archived}, nil
	})
	cctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	jc, err := s.CreateOrUpdateConsumer(cctx, jetstream.ConsumerConfig{
		Durable: "trace", AckPolicy: jetstream.AckExplicitPolicy, AckWait: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := natssink.NewConsumer(jc, target, natssink.ConsumerOptions{Window: 100 * time.Millisecond, MaxRecords: 1})
	if err != nil {
		t.Fatal(err)
	}
	run, stop := context.WithCancel(cctx)
	done := make(chan error, 1)
	go func() { done <- c.Run(run) }()
	for {
		mu.Lock()
		n := len(seen)
		mu.Unlock()
		if n > 0 || cctx.Err() != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("the consumer wrote nothing")
	}
	if got, want := seen[0].TraceID(), root.SpanContext().TraceID(); got != want {
		t.Fatalf("the consumer is in trace %s, the publisher in %s", got, want)
	}
}
