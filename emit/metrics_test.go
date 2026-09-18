package emit_test

import (
	"context"
	"errors"
	"testing"

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

// A best-effort drop is counted: it is the number to alert on, since the design
// allows the drop only on condition that somebody hears about it.
func TestABestEffortDropIsCounted(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	hooks, err := emit.Instrument(emit.Hooks{}, sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	if err != nil {
		t.Fatal(err)
	}
	e := emitter(t, &sink.Memory{Fail: errors.New("the store is unreachable")}, hooks)
	_ = e.Record(context.Background(), viewed()) // best effort
	if err := e.Close(); err != nil {
		t.Log(err)
	}
	if got := counted(t, reader, "audit.emit.records.dropped"); got == 0 {
		t.Fatal("a record best-effort delivery gave up was not counted")
	}
}

func TestTheOutboxDepthIsObservable(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	down := &sink.Memory{Fail: errors.New("the store is unreachable")}
	e, box := outboxEmitter(t, t.TempDir(), down, emit.Hooks{})
	for i := 0; i < 3; i++ {
		if err := e.Record(context.Background(), shipped()); err != nil {
			t.Fatal(err)
		}
	}
	if err := emit.InstrumentOutbox(box, sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))); err != nil {
		t.Fatal(err)
	}
	if got := counted(t, reader, "audit.emit.outbox.pending"); got != 3 {
		t.Fatalf("outbox pending %d, want 3", got)
	}
}
