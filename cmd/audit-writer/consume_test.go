package main

import (
	"context"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	auditv1 "github.com/truvity/audit/sdk/gen/audit/v1"
	"github.com/truvity/audit/sdk/record"
	"github.com/truvity/audit/sdk/sink"
	"github.com/truvity/audit/sink/natssink"
)

const subject = "audit.records"

// opts are the stream settings these tests use. AckWait is long: the stream
// redelivers whatever is not acknowledged within it, so a short one turns a
// machine that stalls for a second between taking a record and acknowledging it
// into a duplicate, and every count in a test into a guess. The one test that
// waits for a redelivery shortens it for itself.
func opts(url string, batch int) streamOptions {
	return streamOptions{
		URL: url, Stream: "AUDIT", Durable: "audit-writer",
		Batch: batch, AckWait: time.Minute,
		// A window shorter than the tests' patience: these exercise the
		// consumption, and the roll has its own tests in sink/natssink.
		Window: 10 * time.Millisecond,
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

// distinct is how many different records the writer has taken. A record the
// stream delivered twice counts once: delivery is at least once, and the
// writer's dedupe is what makes it one.
func (c *target) distinct() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := map[string]bool{}
	for _, id := range c.taken {
		ids[id] = true
	}
	return len(ids)
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

// eventually polls until want holds. The bound is only how long a broken build
// takes to be told so: a working one is not waiting on it, and a loaded
// machine that is merely slow must not be mistaken for a broken one.
func eventually(t *testing.T, want func() bool, why string) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
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
	// The redelivery is the stream's, after the ack wait, so this is the one
	// test that shortens it. A machine slow enough to deliver a record a second
	// time is no failure here, which is why the count below is of distinct
	// records.
	redelivering := opts(url, 10)
	redelivering.AckWait = time.Second
	stop, err := consume(ctx, redelivering, sink.Func(
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

	eventually(t, func() bool { return into.distinct() == 3 },
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

	missing := opts(url, 10)
	missing.Stream = "ABSENT"
	_, err := consume(ctx, missing, &target{})
	if err == nil {
		t.Fatal("a missing stream must stop the writer, not be created by it")
	}
}

// Two replicas share one durable consumer, so a record goes to one of them.
//
// Which replica takes what is the broker's to decide, and on a loaded machine
// one of them can take everything before the other has fetched at all. So the
// test does not hope: each replica's first write waits until the other has
// made one too. That can only end one way if the first cannot hold every
// record: the stream's limit on what is unacknowledged is twice the batch, ten
// of the twenty, and it is shared by both replicas. A replica rolls at five
// records (MaxRecords), so when it stops to wait it holds at most nine, and the
// other is left at least one to take.
func TestTwoWritersShareOneDurableConsumer(t *testing.T) {
	url, js := stream(t)
	publish(t, js, 20)

	first, second := &target{}, &target{}
	arrived := map[*target]chan struct{}{first: make(chan struct{}), second: make(chan struct{})}
	other := map[*target]*target{first: second, second: first}
	once := map[*target]*sync.Once{first: {}, second: {}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, into := range []*target{first, second} {
		shared := opts(url, 5)
		shared.MaxRecords = 5
		stop, err := consume(ctx, shared, sink.Func(
			func(c context.Context, req *sink.Request) (*sink.Result, error) {
				once[into].Do(func() {
					close(arrived[into])
					select {
					case <-arrived[other[into]]:
					case <-time.After(time.Minute):
						t.Error("one replica took everything: the other was never given a record")
					}
				})
				return into.Write(c, req)
			}))
		if err != nil {
			t.Fatal(err)
		}
		defer stop()
	}

	eventually(t, func() bool { return first.count()+second.count() == 20 },
		"the two replicas did not between them take every record")
	if first.count() == 0 || second.count() == 0 {
		t.Fatalf("one replica took everything (%d/%d)", first.count(), second.count())
	}
}
