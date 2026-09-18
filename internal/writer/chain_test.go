package writer_test

import (
	"context"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/truvity/audit/internal/pgtest"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
	"github.com/truvity/audit/sink/natssink"
)

// The write path as one chain, which every other test takes a piece of:
// published to the stream, consumed by the writer, put as locked objects,
// indexed into Postgres, every row addressing the line it came from.
//
// The pieces passing separately says each hop does its job; this says they do
// it together — that what the publisher sends is what the index can find, and
// that the index can find it in the object the digest chain accounts for.
func TestTheWritePathEndToEnd(t *testing.T) {
	pool := pgtest.Open(t)
	b := buildWith(t, pgParts(t, pool, "writer-1", nil))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	srv, err := natsserver.NewServer(&natsserver.Options{Port: -1, JetStream: true, StoreDir: t.TempDir()})
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
	stream, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name: "AUDIT", Subjects: []string{"audit.records"}, Storage: jetstream.FileStorage,
		Discard: jetstream.DiscardNew, Duplicates: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	// The emitter's side: publish, and return only once the stream holds them.
	publisher, err := natssink.NewPublisher(js, natssink.Options{Subject: "audit.records"})
	if err != nil {
		t.Fatal(err)
	}
	sent := map[string]*record.Record{}
	var batch []*record.Record
	for i := 0; i < 5; i++ {
		r := fresh(t)
		sent[r.GetId()] = r
		batch = append(batch, r)
	}
	if _, err := publisher.Write(ctx, &sink.Request{Records: batch, Delivery: sink.Block}); err != nil {
		t.Fatal(err)
	}

	// The writer's side: one durable consumer handing batches to the writer.
	jc, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable: "audit-writer", AckPolicy: jetstream.AckExplicitPolicy, AckWait: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := natssink.NewConsumer(jc, b.writer, natssink.ConsumerOptions{
		Batch: 10, OnError: func(err error) { t.Errorf("the writer refused a batch: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- consumer.Run(runCtx) }()
	defer func() { stop(); <-done }()

	indexed := func() int {
		var n int
		if err := pool.QueryRow(ctx, `select count(*) from events_core where profile = 'security'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for indexed() < len(sent) {
		select {
		case <-ctx.Done():
			t.Fatalf("only %d of %d records reached the index", indexed(), len(sent))
		case <-time.After(50 * time.Millisecond):
		}
	}

	rows, err := pool.Query(ctx, `select id, object_key, line from events_core where profile = 'security'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, key string
		var line int
		if err := rows.Scan(&id, &key, &line); err != nil {
			t.Fatal(err)
		}
		if sent[id] == nil {
			t.Fatalf("the index holds %s, which was never published", id)
		}
		// The row addresses the line that holds the record...
		if got := lineAt(t, b.store, key, line); got.GetId() != id || got.GetAction() != sent[id].GetAction() {
			t.Fatalf("row %s addresses %s:%d, which holds %s", id, key, line, got.GetId())
		}
		// ...in an object under the lock the profile asks for.
		o, ok := b.store.Object(key)
		if !ok || o.RetainUntil.IsZero() {
			t.Fatalf("%s is not locked", key)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
