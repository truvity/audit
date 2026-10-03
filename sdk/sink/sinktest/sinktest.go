// Package sinktest is the conformance suite for a sink.Sink: what every
// implementation owes its callers, checked the same way for each.
//
// A sink's own tests say what is particular to it. This says what is not: that
// the durability it declares is the one it reports, that a record it refuses
// is surfaced rather than swallowed, that an empty batch and a cancelled
// context are handled, that a batch is taken whole, and that a sink claiming
// to be idempotent by record id is.
package sinktest

import (
	"context"
	"testing"

	auditv1 "github.com/truvity/audit/sdk/gen/audit/v1"
	"github.com/truvity/audit/sdk/record"
	"github.com/truvity/audit/sdk/sink"
)

// Subject is a sink under test, and what the suite needs to know about it.
type Subject struct {
	Sink sink.Sink
	// Durability is what the sink must declare and report.
	Durability sink.Durability
	// Count returns how many records the sink holds. Nil skips the checks that
	// need to look inside it.
	Count func() int
	// Idempotent is set by a sink that claims a record sent twice under its id
	// is kept once. It needs Count.
	Idempotent bool
	// Refuses is set by a sink that rejects a record it cannot encode, and
	// the suite then checks the rejection is surfaced.
	Refuses bool
}

// Factory returns a fresh sink, empty, for one test.
type Factory func(t *testing.T) Subject

// Run runs the suite against sinks the factory makes.
func Run(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("declares and reports its durability", func(t *testing.T) {
		s := factory(t)
		if got := sink.Guarantees(s.Sink); got != s.Durability {
			t.Fatalf("Guarantees() = %v, want %v", got, s.Durability)
		}
		if err := sink.Require(s.Sink, s.Durability); err != nil {
			t.Fatalf("Require at its own durability: %v", err)
		}
		if s.Durability < sink.Archived {
			if err := sink.Require(s.Sink, s.Durability+1); err == nil {
				t.Fatal("Require one level above what it declares succeeded")
			}
		}
		res := write(t, s, Records(1))
		if res.Durability != s.Durability {
			t.Fatalf("reported %v, want %v", res.Durability, s.Durability)
		}
		if _, err := sink.Guard(s.Sink, s.Durability); err != nil {
			t.Fatalf("Guard: %v", err)
		}
	})

	t.Run("takes a batch of records whole", func(t *testing.T) {
		s := factory(t)
		const n = 25
		res := write(t, s, Records(n))
		if res.Accepted != n || len(res.Rejected) != 0 {
			t.Fatalf("accepted %d, rejected %d, want %d and none", res.Accepted, len(res.Rejected), n)
		}
		if s.Count != nil && s.Count() != n {
			t.Fatalf("holds %d records, want %d", s.Count(), n)
		}
	})

	t.Run("takes an empty request", func(t *testing.T) {
		s := factory(t)
		res, err := s.Sink.Write(context.Background(), &sink.Request{Delivery: sink.Block})
		if err != nil {
			t.Fatalf("an empty request failed: %v", err)
		}
		if res != nil && (res.Accepted != 0 || len(res.Rejected) != 0) {
			t.Fatalf("an empty request answered %+v", res)
		}
		if s.Count != nil && s.Count() != 0 {
			t.Fatalf("an empty request left %d records", s.Count())
		}
	})

	t.Run("surfaces a refusal", func(t *testing.T) {
		s := factory(t)
		if !s.Refuses {
			t.Skip("the sink does not refuse records")
		}
		good, bad := Records(2), Records(1)[0]
		bad.Source = "\xff" // not UTF-8: no encoding of it exists
		res, err := s.Sink.Write(context.Background(), &sink.Request{
			Records: []*record.Record{good[0], bad, good[1]}, Delivery: sink.Block,
		})
		if err != nil {
			t.Fatalf("a refused record failed the batch: %v", err)
		}
		if res.Accepted != 2 || len(res.Rejected) != 1 || res.Rejected[0].ID != bad.GetId() {
			t.Fatalf("accepted %d, rejected %+v, want 2 accepted and %s rejected", res.Accepted, res.Rejected, bad.GetId())
		}
		if res.Err() == nil {
			t.Fatal("Err() is nil for a result with a rejection")
		}
	})

	t.Run("honours a cancelled context", func(t *testing.T) {
		s := factory(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		batch := Records(3)
		res, err := s.Sink.Write(ctx, &sink.Request{Records: batch, Delivery: sink.Block})
		// Either the sink stops, or it had already taken everything and says
		// so. What it may not do is report success for part of a batch.
		if err == nil && (res == nil || res.Accepted != len(batch)) {
			t.Fatalf("a cancelled write returned no error and %+v", res)
		}
	})

	t.Run("keeps a repeated record once", func(t *testing.T) {
		s := factory(t)
		if !s.Idempotent || s.Count == nil {
			t.Skip("the sink does not claim idempotency by record id")
		}
		batch := Records(5)
		write(t, s, batch)
		write(t, s, batch)
		if got := s.Count(); got != len(batch) {
			t.Fatalf("holds %d records after the same %d were sent twice", got, len(batch))
		}
	})
}

func write(t *testing.T, s Subject, batch []*record.Record) *sink.Result {
	t.Helper()
	res, err := s.Sink.Write(context.Background(), &sink.Request{Records: batch, Delivery: sink.Block})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if res == nil {
		t.Fatal("Write returned neither a result nor an error")
	}
	return res
}

// Records returns n valid records with distinct ids.
func Records(n int) []*record.Record {
	out := make([]*record.Record, 0, n)
	for range n {
		r := &record.Record{
			Source: "shop", Action: "shop.order.placed", TenantId: "acme",
			CatalogueVersion: "1.0.0",
			Operation:        auditv1.Operation_OPERATION_CREATE,
		}
		record.Assign(r)
		out = append(out, r)
	}
	return out
}
