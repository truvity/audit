package emit_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/truvity/audit/emit"
	"github.com/truvity/audit/sink"
)

// counted collects one instrument's total across its data points.
func counted(t *testing.T, reader *sdkmetric.ManualReader, name string) int64 {
	t.Helper()
	var got metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &got); err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, scope := range got.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			switch data := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, p := range data.DataPoints {
					total += p.Value
				}
			case metricdata.Gauge[int64]:
				for _, p := range data.DataPoints {
					total += p.Value
				}
			}
		}
	}
	return total
}

// The instruments count what the emitter did, and the application's own hooks
// still run.
func TestInstrumentCountsAndStillCallsTheHooksItWraps(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	var theirs int
	hooks, err := emit.Instrument(emit.Hooks{OnWritten: func(int, sink.Delivery) { theirs++ }},
		sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	if err != nil {
		t.Fatal(err)
	}
	e := emitter(t, &sink.Memory{}, hooks)
	if err := e.Record(context.Background(), placed()); err != nil { // block
		t.Fatal(err)
	}
	if got := counted(t, reader, "audit.emit.records.written"); got != 1 {
		t.Fatalf("written %d", got)
	}
	if theirs != 1 {
		t.Fatal("the application's own hook did not run")
	}
}

// A drop is counted: it is the number to alert on, since the design
// allows the drop only on condition that somebody hears about it.
func TestABestEffortDropIsCounted(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	hooks, err := emit.Instrument(emit.Hooks{}, sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	if err != nil {
		t.Fatal(err)
	}
	e := emitter(t, &sink.Memory{Fail: errors.New("the store is unreachable")}, hooks)
	_ = e.Record(context.Background(), viewed()) // async
	if err := e.Close(); err != nil {
		t.Log(err)
	}
	if got := counted(t, reader, "audit.emit.records.dropped"); got == 0 {
		t.Fatal("a record the queue gave up was not counted")
	}
}

// An instrumented emitter with no drop hook of its own still says, in the
// log, which record it gave up. Instrument's wrapper is never nil, so New's
// own logger is never installed behind it; the wrapper has to carry it.
// Without that, every deployment that follows the guide -- which is to call
// Instrument -- drops in silence, and the metric is the only witness.
func TestAnInstrumentedDropIsStillLogged(t *testing.T) {
	var lines bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lines, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	reader := sdkmetric.NewManualReader()
	hooks, err := emit.Instrument(emit.Hooks{}, sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	if err != nil {
		t.Fatal(err)
	}
	e := emitter(t, &sink.Memory{Fail: errors.New("the store is unreachable")}, hooks)
	_ = e.Record(context.Background(), viewed()) // async
	if err := e.Close(); err != nil {
		t.Log(err)
	}
	if got := counted(t, reader, "audit.emit.records.dropped"); got == 0 {
		t.Fatal("a record the queue gave up was not counted")
	}
	if !strings.Contains(lines.String(), "given up and is not in the trail") {
		t.Fatalf("the drop was counted and not logged:\n%s", lines.String())
	}
}

// The queue's depth is how much this process would lose if it stopped now, so
// it is the number a deployment watches. Records pile up here exactly while the
// sink will not take them.
func TestTheQueueDepthIsObservable(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	held := make(chan struct{})
	defer close(held)
	stuck := sink.Func(func(_ context.Context, _ *sink.Request) (*sink.Result, error) {
		<-held
		return &sink.Result{}, nil
	})
	e, err := emit.New(emit.Options{
		Source: "shop", Catalogue: shop(t), Sink: stuck,
		Queue: 16, Batch: 100, Flush: time.Hour, // nothing leaves the queue
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := e.Record(context.Background(), viewed()); err != nil {
			t.Fatal(err)
		}
	}
	if err := emit.InstrumentQueue(e, sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))); err != nil {
		t.Fatal(err)
	}
	if got := counted(t, reader, "audit.emit.queue.pending"); got != 3 {
		t.Fatalf("queue pending %d, want 3", got)
	}
}
