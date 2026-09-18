package emit_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/audit/emit"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

const outboxDoc = `
source: shop
version: "1.0.0"
locales: [en]
actor_kinds:
  clerk: { category: internal }
target_types:
  order: { description: "An order." }
actions:
  shop.order.shipped:
    summary: An order was shipped.
    operation: modify
    categories: [data_change]
    profiles: [security]
    target_types: [order]
    delivery: outbox
    message:
      en: "{actor} shipped order {targets_0_id}"
`

func shipped() *record.Record {
	r := viewed()
	r.Action = "shop.order.shipped"
	r.Operation = 3 // OPERATION_MODIFY
	return r
}

func outboxEmitter(t *testing.T, dir string, s sink.Sink, hooks emit.Hooks) (*emit.Emitter, *emit.FileOutbox) {
	t.Helper()
	c := catalogueFrom(t, outboxDoc)
	box, err := emit.OpenFileOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	e, err := emit.New(emit.Options{
		Source: "shop", Catalogue: c, Sink: s, Outbox: box,
		Publish: time.Hour, // the tests drain deliberately
		Hooks:   hooks,
	})
	if err != nil {
		t.Fatal(err)
	}
	return e, box
}

// The record is on the disk before the request completes, so an outage of the
// trail is a delay rather than a loss.
func TestOutboxDeliveryDoesNotWaitForTheSink(t *testing.T) {
	dir := t.TempDir()
	store := &sink.Memory{Fail: errors.New("the store is unreachable")}
	e, box := outboxEmitter(t, dir, store, emit.Hooks{})

	if err := e.Record(context.Background(), shipped()); err != nil {
		t.Fatalf("an outbox record must not fail the caller when the sink is down: %v", err)
	}
	if store.Len() != 0 {
		t.Fatal("nothing should have reached the sink")
	}
	// Durable the moment Append returns, in the open segment; sealing is a
	// matter of when it is delivered, not of whether it is kept.
	if pending, err := box.Pending(); err != nil || pending != 1 {
		t.Fatalf("pending = %d (%v), want the record already on the disk", pending, err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	// Still on the disk after a clean shutdown, because the sink never took it.
	if names := segments(t, dir); len(names) == 0 {
		t.Fatal("a record the sink never took must survive shutdown")
	}
}

// What a process could not deliver, the next one does.
func TestOutboxSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	down := &sink.Memory{Fail: errors.New("the store is unreachable")}
	first, _ := outboxEmitter(t, dir, down, emit.Hooks{})
	for i := 0; i < 3; i++ {
		if err := first.Record(context.Background(), shipped()); err != nil {
			t.Fatal(err)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	up := &sink.Memory{}
	second, box := outboxEmitter(t, dir, up, emit.Hooks{})
	if pending, err := box.Pending(); err != nil || pending != 3 {
		t.Fatalf("pending = %d (%v), want the 3 records the last process left", pending, err)
	}
	if err := second.Close(); err != nil { // closing drains once
		t.Fatal(err)
	}
	if up.Len() != 3 {
		t.Fatalf("the sink took %d of 3 records left by the last process", up.Len())
	}
	if names := segments(t, dir); len(names) != 0 {
		t.Fatalf("delivered segments must be forgotten, found %v", names)
	}
}

// Killing the process between the write and the delivery costs a repeat, not a
// loss, and the writer deduplicates by identifier.
func TestOutboxKeepsWhatWasNeverAcknowledged(t *testing.T) {
	dir := t.TempDir()
	box, err := emit.OpenFileOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := shipped()
	r.Id = record.NewID()
	r.Source, r.CatalogueVersion, r.SchemaVersion = "shop", "1.0.0", record.SchemaVersion
	record.Assign(r)
	if err := box.Append(context.Background(), r); err != nil {
		t.Fatal(err)
	}

	// The sink takes the batch and then fails, as a crash mid-delivery would.
	refuse := errors.New("gone before the acknowledgement")
	err = box.Deliver(context.Background(), func(context.Context, []*record.Record) error { return refuse })
	if !errors.Is(err, refuse) {
		t.Fatalf("Deliver = %v, want the sink's error", err)
	}
	if pending, err := box.Pending(); err != nil || pending != 1 {
		t.Fatalf("pending = %d (%v), want the record kept", pending, err)
	}

	var delivered int
	if err := box.Deliver(context.Background(), func(_ context.Context, batch []*record.Record) error {
		delivered += len(batch)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if delivered != 1 {
		t.Fatalf("the second pass delivered %d records", delivered)
	}
	if pending, _ := box.Pending(); pending != 0 {
		t.Fatalf("pending = %d after delivery", pending)
	}
	if err := box.Close(); err != nil {
		t.Fatal(err)
	}
}

// An audit trail that arrives out of order is one nobody can reason about, so a
// failed batch stops the pass rather than letting later records overtake it.
func TestOutboxStopsAtTheFirstRefusal(t *testing.T) {
	dir := t.TempDir()
	box, err := emit.OpenFileOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close() //nolint:errcheck // the test asserts on Deliver

	for _, id := range []string{"first", "second"} {
		r := shipped()
		r.Id, r.Source, r.CatalogueVersion = record.NewID(), "shop", "1.0.0"
		r.Attributes = map[string]string{"batch": id}
		record.Assign(r)
		if err := box.Append(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		// Sealing between the two puts them in separate segments.
		if err := box.Deliver(context.Background(), func(context.Context, []*record.Record) error {
			return errors.New("not yet")
		}); err == nil {
			t.Fatal("want the refusal")
		}
	}

	var seen []string
	err = box.Deliver(context.Background(), func(_ context.Context, batch []*record.Record) error {
		for _, r := range batch {
			seen = append(seen, r.GetAttributes()["batch"])
		}
		if len(seen) == 1 {
			return errors.New("still not")
		}
		return nil
	})
	if err == nil {
		t.Fatal("want the refusal")
	}
	if len(seen) != 1 || seen[0] != "first" {
		t.Fatalf("saw %v, want only the oldest segment before stopping", seen)
	}
}

// A line this build cannot decode must not wedge every record behind it.
func TestOutboxSkipsALineItCannotRead(t *testing.T) {
	dir := t.TempDir()
	box, err := emit.OpenFileOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := shipped()
	r.Id, r.Source, r.CatalogueVersion = record.NewID(), "shop", "1.0.0"
	record.Assign(r)
	if err := box.Append(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if err := box.Close(); err != nil {
		t.Fatal(err)
	}

	names := segments(t, dir)
	if len(names) != 1 {
		t.Fatalf("segments = %v", names)
	}
	path := filepath.Join(dir, names[0])
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append([]byte("{ this is not a record\n"), body...), 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, err := emit.OpenFileOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close() //nolint:errcheck // asserted through Deliver
	var delivered int
	if err := reopened.Deliver(context.Background(), func(_ context.Context, batch []*record.Record) error {
		delivered += len(batch)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if delivered != 1 {
		t.Fatalf("delivered %d records; the good one should have gone through", delivered)
	}
}

// Without an outbox the emitter refuses a catalogue that asks for one, rather
// than quietly delivering it some other way.
func TestOutboxDeliveryNeedsAnOutbox(t *testing.T) {
	_, err := emit.New(emit.Options{
		Source: "shop", Catalogue: catalogueFrom(t, outboxDoc), Sink: &sink.Memory{},
	})
	if err == nil || !strings.Contains(err.Error(), "no outbox is configured") {
		t.Fatalf("want a refusal naming the missing outbox, got %v", err)
	}
}

func TestOutboxReportsWhatIsWaiting(t *testing.T) {
	dir := t.TempDir()
	store := &sink.Memory{Fail: errors.New("down")}
	var failures int
	var mu sync.Mutex
	e, box := outboxEmitter(t, dir, store, emit.Hooks{
		OnFailed: func(error, sink.Delivery, int) { mu.Lock(); failures++; mu.Unlock() },
	})
	for i := 0; i < 5; i++ {
		if err := e.Record(context.Background(), shipped()); err != nil {
			t.Fatal(err)
		}
	}
	if pending, err := box.Pending(); err != nil || pending != 5 {
		t.Fatalf("pending = %d (%v), want 5", pending, err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if failures == 0 {
		t.Fatal("a sink that will not take the outbox must be reported")
	}
}

func segments(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "sealed-") {
			names = append(names, e.Name())
		}
	}
	return names
}
