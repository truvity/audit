package emit_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/truvity/audit/emit"
	"github.com/truvity/audit/sink"
)

// A drop nobody wired a hook for is still said somewhere: the emitter writes
// it to the log, with enough to find the record in the application's own
// evidence. Without this, "the record is also a log line" would be a promise
// the emitter made on the application's behalf.
func TestADropIsLoggedWhenNobodyListens(t *testing.T) {
	var out bytes.Buffer
	held := make(chan struct{})
	defer close(held)
	called := make(chan struct{}, 1)
	stuck := sink.Func(func(_ context.Context, _ *sink.Request) (*sink.Result, error) {
		called <- struct{}{}
		<-held
		return &sink.Result{}, nil
	})
	e, err := emit.New(emit.Options{
		Source: "shop", Catalogue: shop(t), Sink: stuck,
		Queue: 1, Batch: 1, Flush: time.Hour,
		Logger: slog.New(slog.NewTextHandler(&out, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	// The first record is taken off the queue and handed to the sink, which
	// never answers, so the loop is stuck delivering it.
	if err := e.Record(context.Background(), viewed()); err != nil {
		t.Fatal(err)
	}
	<-called
	// The second waits in the queue, which holds one. The third has nowhere
	// to go, and the oldest waiting record — the second — is given up.
	second := viewed()
	if err := e.Record(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := e.Record(context.Background(), viewed()); err != nil {
		t.Fatal(err)
	}
	logged := out.String()
	if !strings.Contains(logged, second.GetId()) || !strings.Contains(logged, "queue is full") {
		t.Fatalf("the dropped record should be in the log with its id and the reason, got: %q", logged)
	}
}
