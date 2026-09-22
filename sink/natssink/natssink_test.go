package natssink_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
	"github.com/truvity/audit/sink/natssink"
)

const subject = "audit.records"

func one(t *testing.T) *record.Record {
	t.Helper()
	r := &record.Record{
		Source: "shop", Action: "shop.order.placed", TenantId: "acme",
		CatalogueVersion: "1.0.0",
		Operation:        auditv1.Operation_OPERATION_CREATE,
	}
	record.Assign(r)
	return r
}

// stream starts an in-process NATS server with JetStream and returns a
// publisher and a consumer bound to one stream.
func stream(t *testing.T) (jetstream.JetStream, jetstream.Stream, *nats.Conn) {
	t.Helper()
	opts := &natsserver.Options{
		Port:      -1,
		JetStream: true,
		StoreDir:  t.TempDir(),
	}
	srv, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("the test server did not start")
	}
	t.Cleanup(srv.Shutdown)

	conn, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)

	js, err := jetstream.New(conn)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name:     "AUDIT",
		Subjects: []string{subject},
		Storage:  jetstream.FileStorage,
		// A stalled writer must surface to the publisher as a refusal rather
		// than quietly dropping the oldest records, which is what an audit
		// stream needs and the default does not give.
		Discard:    jetstream.DiscardNew,
		Duplicates: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return js, s, conn
}

// A publish that returns means the record is on the stream's disks. That
// acknowledgement is the whole of what block delivery promises.
func TestPublishWaitsForTheStream(t *testing.T) {
	js, s, _ := stream(t)
	p, err := natssink.NewPublisher(js, natssink.Options{Subject: subject})
	if err != nil {
		t.Fatal(err)
	}

	res, err := p.Write(context.Background(), &sink.Request{
		Records: []*record.Record{one(t), one(t)}, Delivery: sink.Block,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 2 {
		t.Fatalf("accepted %d, want 2", res.Accepted)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := s.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.State.Msgs != 2 {
		t.Fatalf("the stream holds %d messages, want 2", info.State.Msgs)
	}
}

// A publisher that did not see an acknowledgement may send again. The stream's
// duplicate window absorbs it, which is what makes the queue's repeats and the
// emitter's retries harmless.
func TestRepublishingARecordIsHarmless(t *testing.T) {
	js, s, _ := stream(t)
	p, _ := natssink.NewPublisher(js, natssink.Options{Subject: subject})

	r := one(t)
	for i := 0; i < 3; i++ {
		if _, err := p.Write(context.Background(), &sink.Request{Records: []*record.Record{r}}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := s.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.State.Msgs != 1 {
		t.Fatalf("the stream holds %d copies of one record, want 1", info.State.Msgs)
	}
}

// The same contract on both sides: what a publisher put on the stream reaches
// the sink on the other side unchanged.
func TestRoundTripThroughTheStream(t *testing.T) {
	js, s, _ := stream(t)
	p, _ := natssink.NewPublisher(js, natssink.Options{Subject: subject})

	sent := []*record.Record{one(t), one(t), one(t)}
	if _, err := p.Write(context.Background(), &sink.Request{Records: sent}); err != nil {
		t.Fatal(err)
	}

	store := &sink.Memory{}
	run(t, s, store, nil, func() bool { return distinct(store.Records()) == len(sent) })

	// Every record arrived, and none was altered on the way. What is NOT
	// asserted is that exactly three arrived: a stream is at-least-once by
	// design, and under load the acknowledgement of a batch can lose the race
	// with its own redelivery. Asserting exactly-once here would be asserting
	// a property the design deliberately does not have — it is the writer's
	// deduplication table that makes a repeat cost nothing, not the transport.
	seen := map[string]bool{}
	for _, r := range store.Records() {
		seen[r.GetId()] = true
		if r.GetAction() != "shop.order.placed" {
			t.Fatalf("a record did not survive the crossing: %+v", r)
		}
	}
	for _, r := range sent {
		if !seen[r.GetId()] {
			t.Fatalf("record %s never arrived", r.GetId())
		}
	}
}

// distinct counts records by identifier, which is the number a reader of the
// archive would see once the writer has absorbed the repeats.
func distinct(rows []*record.Record) int {
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.GetId()] = true
	}
	return len(seen)
}

// Nothing is lost to a writer that was briefly unable to write: unacknowledged
// messages come back.
func TestAFailingTargetGetsTheRecordsAgain(t *testing.T) {
	js, s, _ := stream(t)
	p, _ := natssink.NewPublisher(js, natssink.Options{Subject: subject})
	if _, err := p.Write(context.Background(), &sink.Request{Records: []*record.Record{one(t)}}); err != nil {
		t.Fatal(err)
	}

	var (
		mu       sync.Mutex
		attempts int
	)
	store := &sink.Memory{}
	target := sink.Func(func(ctx context.Context, req *sink.Request) (*sink.Result, error) {
		mu.Lock()
		attempts++
		refuse := attempts <= 2
		mu.Unlock()
		if refuse {
			return nil, errors.New("the store is unreachable")
		}
		return store.Write(ctx, req)
	})

	var failures int
	run(t, s, target, func(error) { mu.Lock(); failures++; mu.Unlock() },
		func() bool { return store.Len() == 1 })

	mu.Lock()
	defer mu.Unlock()
	if attempts < 3 {
		t.Fatalf("the target saw %d attempts; the stream should have brought the record back", attempts)
	}
	if failures == 0 {
		t.Fatal("a refused batch must be reported, or records go round silently")
	}
}

// A message this build cannot decode would wedge the stream behind it. It is
// acknowledged and reported instead; a dead letter belongs to the writer, which
// is the one with a bucket.
func TestAnUndecodableMessageDoesNotWedgeTheStream(t *testing.T) {
	js, s, conn := stream(t)
	if _, err := js.Publish(context.Background(), subject, []byte("{ not a record")); err != nil {
		t.Fatal(err)
	}
	p, _ := natssink.NewPublisher(js, natssink.Options{Subject: subject})
	if _, err := p.Write(context.Background(), &sink.Request{Records: []*record.Record{one(t)}}); err != nil {
		t.Fatal(err)
	}
	_ = conn

	store := &sink.Memory{}
	var reported int
	var mu sync.Mutex
	run(t, s, store, func(error) { mu.Lock(); reported++; mu.Unlock() },
		func() bool { return store.Len() == 1 })

	mu.Lock()
	defer mu.Unlock()
	if reported == 0 {
		t.Fatal("skipping a message must be reported")
	}
}

func TestNewChecksItsArguments(t *testing.T) {
	js, _, _ := stream(t)
	if _, err := natssink.NewPublisher(nil, natssink.Options{Subject: subject}); err == nil {
		t.Error("want a refusal with no JetStream context")
	}
	if _, err := natssink.NewPublisher(js, natssink.Options{}); err == nil {
		t.Error("want a refusal with no subject")
	}
	if _, err := natssink.NewConsumer(nil, &sink.Memory{}, natssink.ConsumerOptions{}); err == nil {
		t.Error("want a refusal with no consumer")
	}
}

// run binds a durable consumer, runs it until want is satisfied, and stops.
func run(t *testing.T, s jetstream.Stream, target sink.Sink, onError func(error), want func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	jc, err := s.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:    "writer",
		AckPolicy:  jetstream.AckExplicitPolicy,
		AckWait:    time.Second,
		MaxDeliver: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := natssink.NewConsumer(jc, target, natssink.ConsumerOptions{Batch: 10, OnError: onError})
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
			t.Fatal("the consumer did not deliver what was expected in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatalf("the consumer stopped with %v", err)
	}
}
