package main

import (
	"context"
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

// opts are the stream settings these tests use. AckWait is a second rather than
// the half minute a deployment wants, so that a redelivery can be waited for.
func opts(url string, batch int) streamOptions {
	return streamOptions{
		URL: url, Stream: "AUDIT", Durable: "audit-writer",
		Batch: batch, AckWait: time.Second,
	}
}

// stream starts an embedded JetStream server with the wide stream on it.
func stream(t *testing.T) (string, jetstream.JetStream) {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Port:      -1,
		JetStream: true,
		StoreDir:  t.TempDir(),
	})
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
	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name: "AUDIT", Subjects: []string{subject}, Storage: jetstream.FileStorage,
	}); err != nil {
		t.Fatal(err)
	}
	return srv.ClientURL(), js
}

// target counts what reached the writer, and can refuse once.
type target struct {
	mu      sync.Mutex
	taken   []string
	refuse  int
	refused error
}

func (c *target) Write(_ context.Context, req *sink.Request) (*sink.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refuse > 0 {
		c.refuse--
		return nil, c.refused
	}
	for _, r := range req.Records {
		c.taken = append(c.taken, r.GetId())
	}
	return &sink.Result{Accepted: len(req.Records)}, nil
}

func (c *target) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.taken)
}

func publish(t *testing.T, js jetstream.JetStream, n int) {
	t.Helper()
	p, err := natssink.NewPublisher(js, natssink.Options{Subject: subject})
	if err != nil {
		t.Fatal(err)
	}
	records := make([]*record.Record, 0, n)
	for i := 0; i < n; i++ {
		r := &record.Record{
			Id: record.NewID(), SchemaVersion: record.SchemaVersion,
			CatalogueVersion: "1.0.0", Source: "wallet", TenantId: "acme",
			Action: "wallet.credential.issued", Operation: auditv1.Operation_OPERATION_CREATE,
		}
		record.Assign(r)
		records = append(records, r)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := p.Write(ctx, &sink.Request{Records: records, Delivery: sink.Block}); err != nil {
		t.Fatal(err)
	}
}

func eventually(t *testing.T, want func() bool, why string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if want() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(why)
}

// The writer behind a stream is the shape the deployment has: records reach it
// from the stream, not only from a caller holding its address.
func TestConsumeCarriesTheStreamToTheWriter(t *testing.T) {
	url, js := stream(t)
	publish(t, js, 5)

	into := &target{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, err := consume(ctx, opts(url, 10), into)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	eventually(t, func() bool { return into.count() == 5 }, "the stream did not reach the writer")
}

// A writer that cannot write must leave its messages for the redelivery. That
// is the whole reason the stream is between the emitter and the archive, and it
// is the one property no amount of retrying above can supply.
func TestARefusedBatchComesBack(t *testing.T) {
	url, js := stream(t)
	publish(t, js, 3)

	into := &target{refuse: 1, refused: context.DeadlineExceeded}
	var reported int
	var mu sync.Mutex

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, err := consume(ctx, opts(url, 10), sink.Func(
		func(c context.Context, req *sink.Request) (*sink.Result, error) {
			res, err := into.Write(c, req)
			if err != nil {
				mu.Lock()
				reported++
				mu.Unlock()
			}
			return res, err
		}))
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	eventually(t, func() bool { return into.count() == 3 },
		"a batch the writer refused was not redelivered")
	mu.Lock()
	defer mu.Unlock()
	if reported == 0 {
		t.Fatal("the refusal was not reported, so a deployment would not know records were going round")
	}
}

// The stream is the deployment's to create. A writer that created one would be
// deciding its retention and its discard policy, which are exactly the choices
// that decide whether a full stream refuses publishers or drops records.
func TestConsumeRefusesAStreamThatIsNotThere(t *testing.T) {
	url, _ := stream(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := consume(ctx, streamOptions{URL: url, Stream: "ABSENT", Durable: "audit-writer", Batch: 10, AckWait: time.Second}, &target{})
	if err == nil {
		t.Fatal("a missing stream must stop the writer, not be created by it")
	}
}

// Two replicas share one durable consumer, so a record goes to one of them.
func TestTwoWritersShareOneDurableConsumer(t *testing.T) {
	url, js := stream(t)
	publish(t, js, 20)

	first, second := &target{}, &target{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, into := range []*target{first, second} {
		stop, err := consume(ctx, opts(url, 5), into)
		if err != nil {
			t.Fatal(err)
		}
		defer stop()
	}

	eventually(t, func() bool { return first.count()+second.count() == 20 },
		"the two replicas did not between them take every record")
	if first.count() == 0 || second.count() == 0 {
		t.Skipf("one replica took everything (%d/%d), which is allowed but proves nothing here",
			first.count(), second.count())
	}
}
