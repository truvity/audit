package emit_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	auditv1 "github.com/truvity/audit/gen/audit/v1"

	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/emit"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

const doc = `
source: shop
version: "1.0.0"
locales: [en]
actor_kinds:
  clerk: { category: internal }
target_types:
  order: { description: "An order." }
meters:
  orders:
    kind: count
    unit: order
actions:
  shop.order.placed:
    summary: An order was placed.
    operation: create
    categories: [data_change, billing]
    profiles: [security, billing]
    target_types: [order]
    delivery: block
    message:
      en: "{actor} placed order {targets_0_id}"
    meter:
      name: orders
  shop.order.viewed:
    summary: An order was read.
    operation: access
    categories: [data_access]
    profiles: [security]
    target_types: [order]
    delivery: best_effort
    message:
      en: "{actor} read order {targets_0_id}"
`

func shop(t *testing.T) *catalogue.Catalogue { return catalogueFrom(t, doc) }

func catalogueFrom(t *testing.T, document string) *catalogue.Catalogue {
	t.Helper()
	c, err := catalogue.Load([]byte(document), nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func placed() *record.Record {
	return &record.Record{
		Action:    "shop.order.placed",
		Operation: auditv1.Operation_OPERATION_CREATE,
		TenantId:  "acme",
		Actor:     &record.Actor{Kind: "clerk", Id: "c_1"},
		Targets:   []*record.Target{{Type: "order", Id: "o_1"}},
		Meter: &record.Meter{
			Name: "orders", Quantity: "1", Unit: "order", Kind: auditv1.Meter_KIND_COUNT,
		},
	}
}

func viewed() *record.Record {
	return &record.Record{
		Action:    "shop.order.viewed",
		Operation: auditv1.Operation_OPERATION_ACCESS,
		TenantId:  "acme",
		Actor:     &record.Actor{Kind: "clerk", Id: "c_1"},
		Targets:   []*record.Target{{Type: "order", Id: "o_1"}},
	}
}

func emitter(t *testing.T, s sink.Sink, hooks emit.Hooks) *emit.Emitter {
	t.Helper()
	e, err := emit.New(emit.Options{
		Source: "shop", Catalogue: shop(t), Sink: s,
		Version: "1.2.3", Instance: "shop-abc",
		Flush: 10 * time.Millisecond, Hooks: hooks,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

// The emitter fills what only it knows, and the caller supplies what only the
// application knows.
func TestRecordFillsWhatTheEmitterKnows(t *testing.T) {
	store := &sink.Memory{}
	e := emitter(t, store, emit.Hooks{})

	r := placed()
	if err := e.Record(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if r.GetId() == "" || r.GetOccurredAt() == nil {
		t.Fatal("the emitter must mint the identifier and the time")
	}
	if r.GetSource() != "shop" || r.GetCatalogueVersion() != "1.0.0" {
		t.Fatalf("source %q catalogue %q", r.GetSource(), r.GetCatalogueVersion())
	}
	if r.GetSchemaVersion() != record.SchemaVersion {
		t.Fatalf("schema version %q", r.GetSchemaVersion())
	}
	if r.GetObserver().GetVersion() != "1.2.3" || r.GetObserver().GetInstance() != "shop-abc" {
		t.Fatalf("observer = %+v", r.GetObserver())
	}
	// The writer stamps the verified identity; the emitter must not claim one.
	if r.GetObserver().GetId() != "" {
		t.Fatal("an emitter may not claim an observer identity")
	}
	if store.Len() != 1 {
		t.Fatalf("the sink holds %d records", store.Len())
	}
}

// The operation is the catalogue's: a caller need not repeat it, and cannot
// contradict it.
func TestRecordTakesTheOperationFromTheCatalogue(t *testing.T) {
	store := &sink.Memory{}
	e := emitter(t, store, emit.Hooks{})

	r := placed()
	r.Operation = auditv1.Operation_OPERATION_UNSPECIFIED
	if err := e.Record(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if r.GetOperation() != auditv1.Operation_OPERATION_CREATE {
		t.Fatalf("operation = %v, want the catalogue's create", r.GetOperation())
	}

	r = placed()
	r.Operation = auditv1.Operation_OPERATION_REMOVE
	if err := e.Record(context.Background(), r); !errors.Is(err, emit.ErrRefused) {
		t.Fatalf("an operation the catalogue does not declare must be refused, got %v", err)
	}
}

// A producer's sequence is what lets a reader see a gap.
func TestSequenceIsMonotonicPerEmitter(t *testing.T) {
	store := &sink.Memory{}
	e := emitter(t, store, emit.Hooks{})
	for i := 0; i < 5; i++ {
		if err := e.Record(context.Background(), placed()); err != nil {
			t.Fatal(err)
		}
	}
	for i, r := range store.Records() {
		if want := uint64(i + 1); r.GetSequence() != want {
			t.Fatalf("record %d has sequence %d, want %d", i, r.GetSequence(), want)
		}
	}
}

// An action declared block does not complete until its record is durable. This
// is the whole of the fail-closed guarantee.
func TestBlockDeliveryFailsTheCaller(t *testing.T) {
	store := &sink.Memory{Fail: errors.New("the store is unreachable")}
	var failed int
	e := emitter(t, store, emit.Hooks{OnFailed: func(error, sink.Delivery, int) { failed++ }})

	err := e.Record(context.Background(), placed())
	if err == nil {
		t.Fatal("a blocking record that did not become durable must fail the caller")
	}
	if !strings.Contains(err.Error(), "did not become durable") {
		t.Fatalf("the error should say what went wrong: %v", err)
	}
	if failed != 1 {
		t.Fatalf("the failure hook fired %d times", failed)
	}
}

// A request whose client has gone away still recorded what it did.
func TestBlockDeliverySurvivesACancelledCaller(t *testing.T) {
	store := &sink.Memory{}
	e := emitter(t, store, emit.Hooks{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.Record(ctx, placed()); err != nil {
		t.Fatalf("a cancelled caller must not lose the record: %v", err)
	}
	if store.Len() != 1 {
		t.Fatal("the record was not written")
	}
}

// Best-effort records are batched, and the caller does not wait for them.
func TestBestEffortDeliveryIsBatched(t *testing.T) {
	store := &sink.Memory{}
	var written int
	var mu sync.Mutex
	e := emitter(t, store, emit.Hooks{OnWritten: func(n int, _ sink.Delivery) {
		mu.Lock()
		written += n
		mu.Unlock()
	}})
	for i := 0; i < 3; i++ {
		if err := e.Record(context.Background(), viewed()); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if store.Len() != 3 {
		t.Fatalf("the sink holds %d records, want 3", store.Len())
	}
	mu.Lock()
	defer mu.Unlock()
	if written != 3 {
		t.Fatalf("the written hook counted %d", written)
	}
}

// What is given up is given up loudly: a deployment that does not alert on this
// has no idea what it is missing.
func TestBestEffortDropsLoudly(t *testing.T) {
	var dropped []string
	var mu sync.Mutex
	blocked := make(chan struct{})
	slow := sink.Func(func(_ context.Context, _ *sink.Request) (*sink.Result, error) {
		<-blocked
		return &sink.Result{}, nil
	})
	e, err := emit.New(emit.Options{
		Source: "shop", Catalogue: shop(t), Sink: slow,
		Queue: 1, Batch: 1, Flush: time.Millisecond,
		Hooks: emit.Hooks{OnDropped: func(_ *record.Record, reason string) {
			mu.Lock()
			dropped = append(dropped, reason)
			mu.Unlock()
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		if err := e.Record(context.Background(), viewed()); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	n := len(dropped)
	mu.Unlock()
	close(blocked)
	_ = e.Close()
	if n == 0 {
		t.Fatal("a full queue must report what it gave up")
	}
	if !strings.Contains(dropped[0], "queue is full") {
		t.Fatalf("the reason should say why: %q", dropped[0])
	}
}

// A record the catalogue does not describe is a fault in the emitting code, and
// the emitter says so rather than writing it.
func TestRecordRefusesWhatTheCatalogueDoesNotDescribe(t *testing.T) {
	store := &sink.Memory{}
	var refused int
	e := emitter(t, store, emit.Hooks{OnRefused: func(*record.Record, error) { refused++ }})

	for _, tc := range []struct {
		name string
		edit func(*record.Record)
		want string
	}{
		{"an action nobody declared", func(r *record.Record) { r.Action = "shop.order.burned" }, "declares no action"},
		{"an actor kind nobody declared", func(r *record.Record) { r.Actor.Kind = "ghost" }, "not declared"},
		{"no tenant", func(r *record.Record) { r.TenantId = "" }, "tenant_id is required"},
		{"a secret in an attribute", func(r *record.Record) {
			r.Attributes = map[string]string{"client_secret": "shh"}
		}, "x-audit-sensitive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := placed()
			tc.edit(r)
			err := e.Record(context.Background(), r)
			if err == nil {
				t.Fatal("want refusal")
			}
			if !errors.Is(err, emit.ErrRefused) {
				t.Fatalf("the error should be recognisable: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
	if store.Len() != 0 {
		t.Fatal("a refused record must not reach the sink")
	}
	if refused == 0 {
		t.Fatal("the refusal hook never fired")
	}
}

// An emitter that quietly downgrades a delivery mode is worse than one that
// will not start.
func TestNewRefusesADeliveryItCannotProvide(t *testing.T) {
	c, err := catalogue.Load([]byte(strings.Replace(doc, "delivery: block", "delivery: outbox", 1)), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = emit.New(emit.Options{Source: "shop", Catalogue: c, Sink: &sink.Memory{}})
	if err == nil || !strings.Contains(err.Error(), "outbox") {
		t.Fatalf("want a refusal naming the delivery it cannot provide, got %v", err)
	}
}

func TestNewChecksItsArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts emit.Options
		want string
	}{
		{"no source", emit.Options{Catalogue: shop(t), Sink: &sink.Memory{}}, "source is required"},
		{"no catalogue", emit.Options{Source: "shop", Sink: &sink.Memory{}}, "catalogue is required"},
		{"no sink", emit.Options{Source: "shop", Catalogue: shop(t)}, "sink is required"},
		{"another source's catalogue", emit.Options{Source: "other", Catalogue: shop(t), Sink: &sink.Memory{}}, "catalogue of"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := emit.New(tc.opts); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

// Closing writes what is still queued; exiting without closing does not.
func TestCloseDrainsTheQueue(t *testing.T) {
	store := &sink.Memory{}
	e, err := emit.New(emit.Options{
		Source: "shop", Catalogue: shop(t), Sink: store,
		Flush: time.Hour, // nothing would be flushed by time
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := e.Record(context.Background(), viewed()); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if store.Len() != 4 {
		t.Fatalf("closing wrote %d of 4 queued records", store.Len())
	}
	if err := e.Close(); err != nil {
		t.Fatalf("closing twice must be safe: %v", err)
	}
}
