package natssink_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
	"github.com/truvity/audit/sink/natssink"
)

// writes records what the target was handed, batch by batch, which is what a
// roll decides: one call here is one object in the archive.
type writes struct {
	mu    sync.Mutex
	sizes []int
}

func (w *writes) Write(_ context.Context, req *sink.Request) (*sink.Result, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sizes = append(w.sizes, len(req.Records))
	return &sink.Result{Accepted: len(req.Records)}, nil
}

func (w *writes) calls() []int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]int(nil), w.sizes...)
}

func (w *writes) total() int {
	n := 0
	for _, s := range w.calls() {
		n += s
	}
	return n
}

// publish puts n records on the stream.
func publish(t *testing.T, js jetstream.JetStream, n int) {
	t.Helper()
	p, err := natssink.NewPublisher(js, natssink.Options{Subject: subject})
	if err != nil {
		t.Fatal(err)
	}
	records := make([]*record.Record, 0, n)
	for i := 0; i < n; i++ {
		records = append(records, one(t))
	}
	if _, err := p.Write(context.Background(), &sink.Request{Records: records}); err != nil {
		t.Fatal(err)
	}
}

// consume runs a consumer with the given roll until want is true, then stops.
func consume(t *testing.T, s jetstream.Stream, target sink.Sink, o natssink.ConsumerOptions, want func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	jc, err := s.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable: "roll", AckPolicy: jetstream.AckExplicitPolicy,
		AckWait: 30 * time.Second, MaxDeliver: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	o.OnError = func(err error) { t.Log(err) }
	c, err := natssink.NewConsumer(jc, target, o)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- c.Run(runCtx) }()

	deadline := time.Now().Add(15 * time.Second)
	for !want() {
		if time.Now().After(deadline) {
			stop()
			<-done
			t.Fatal("the consumer did not roll as expected in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	<-done
}

// A count reached mid-window writes at once: waiting longer would only make
// the object larger than the deployment asked for.
func TestTheRollClosesOnTheRecordCount(t *testing.T) {
	js, s, _ := stream(t)
	publish(t, js, 10)

	target := &writes{}
	consume(t, s, target, natssink.ConsumerOptions{
		Batch: 10, MaxRecords: 4, Window: time.Hour,
	}, func() bool { return target.total() >= 8 })

	for _, n := range target.calls() {
		if n > 4 {
			t.Errorf("a batch of %d records, want no more than the 4 asked for: %v", n, target.calls())
		}
	}
}

// Size is the other bound, for records that are large rather than many.
func TestTheRollClosesOnBytes(t *testing.T) {
	js, s, _ := stream(t)
	publish(t, js, 10)

	target := &writes{}
	consume(t, s, target, natssink.ConsumerOptions{
		// Smaller than two records, so every second one closes the batch.
		Batch: 10, MaxBytes: 1, Window: time.Hour,
	}, func() bool { return target.total() >= 4 })

	for _, n := range target.calls() {
		if n != 1 {
			t.Errorf("a batch of %d records, want 1 with a byte limit below one record: %v", n, target.calls())
		}
	}
}

// A quiet stream still closes its window, or the last records of a slow day
// would wait for the next one.
func TestTheRollClosesOnTheWindow(t *testing.T) {
	js, s, _ := stream(t)
	publish(t, js, 3)

	target := &writes{}
	consume(t, s, target, natssink.ConsumerOptions{
		Batch: 10, MaxRecords: 1000, Window: 50 * time.Millisecond,
	}, func() bool { return target.total() == 3 })

	if calls := target.calls(); len(calls) != 1 {
		t.Errorf("%d writes, want the window to have gathered all three into one: %v", len(calls), calls)
	}
}

// A window that outlasts the stream's ack wait would have the stream redeliver
// records the consumer is still gathering, and write them twice. The consumer
// refuses rather than doing that quietly.
func TestAWindowLongerThanTheAckWaitIsRefused(t *testing.T) {
	_, s, _ := stream(t)
	ctx := context.Background()
	jc, err := s.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable: "guard", AckPolicy: jetstream.AckExplicitPolicy, AckWait: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = natssink.NewConsumer(jc, &sink.Memory{}, natssink.ConsumerOptions{
		Window: 2 * time.Second, AckWait: time.Second,
	})
	if err == nil {
		t.Fatal("a window longer than the ack wait was accepted")
	}
	if !errors.Is(err, err) || len(err.Error()) == 0 {
		t.Fatal("the refusal says nothing")
	}
}
