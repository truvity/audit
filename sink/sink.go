// Package sink is the write contract, and it is the same contract at every
// hop.
//
// An emitter calls a Sink. A queue publisher implements one. A queue consumer
// calls one on the split writer. An adapter outside the cluster calls one over
// Connect. Neither end knows whether there is a queue in between, which is what
// lets a deployment put one there, or take it away, without touching either.
package sink

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/record"
)

// Delivery is how much the caller needs to know before it carries on.
type Delivery = auditv1.Delivery

const (
	// Block returns only once the records are durable at the next hop. The
	// business request that caused them does not complete until then, which is
	// what makes a privileged or billable action fail rather than go
	// unrecorded.
	Block = auditv1.Delivery_DELIVERY_BLOCK
	// Async returns at once and leaves the waiting to the emitter, which keeps
	// the record in a bounded queue and retries until this hop acknowledges
	// it. The acknowledgement means the same thing either way: durable.
	Async = auditv1.Delivery_DELIVERY_ASYNC
)

// ParseDelivery reads the spelling a catalogue uses. There are two, and the
// two it replaced are refused by name rather than quietly mapped: an action
// declared `outbox` was written expecting a promise this component no longer
// makes, and its author should say which of the two it wants.
func ParseDelivery(s string) (Delivery, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "block":
		return Block, nil
	case "async", "":
		return Async, nil
	case "outbox":
		return auditv1.Delivery_DELIVERY_UNSPECIFIED, errors.New(
			`sink: "outbox" is retired. Declare "block" where the action may not go unrecorded, ` +
				`and "async" otherwise: there is no file on the pod any more, and an async record ` +
				`is retried from memory until it is acknowledged`)
	case "best_effort", "best-effort":
		return auditv1.Delivery_DELIVERY_UNSPECIFIED, errors.New(
			`sink: "best_effort" is retired. Declare "async", which keeps the record and retries ` +
				`until it is acknowledged instead of giving it up under pressure`)
	default:
		return auditv1.Delivery_DELIVERY_UNSPECIFIED, fmt.Errorf("sink: %q is not a delivery mode", s)
	}
}

// Durability is what an acknowledgement promises about the records it covers,
// ordered so that a higher value survives more. See
// docs/decisions/0017-sink-durability-and-transports.md.
type Durability = auditv1.Durability

const (
	// Unspecified is no answer, and counts as the weakest: a hop that does not
	// say how durable its acknowledgement is can never satisfy a requirement.
	Unspecified = auditv1.Durability_DURABILITY_UNSPECIFIED
	// Logged means the process wrote the records to its log and nothing else.
	Logged = auditv1.Durability_DURABILITY_LOGGED
	// Queued means a durable, replicated queue holds the records and will
	// deliver them to the archive.
	Queued = auditv1.Durability_DURABILITY_QUEUED
	// Archived means the records are in the archive's bucket.
	Archived = auditv1.Durability_DURABILITY_ARCHIVED
)

// ParseDurability reads the spelling a configuration uses: "logged", "queued"
// or "archived".
func ParseDurability(s string) (Durability, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "logged":
		return Logged, nil
	case "queued":
		return Queued, nil
	case "archived":
		return Archived, nil
	default:
		return Unspecified, fmt.Errorf("sink: %q is not a durability: use logged, queued or archived", s)
	}
}

func durabilityName(d Durability) string {
	if d == Unspecified {
		return "unspecified"
	}
	return strings.ToLower(strings.TrimPrefix(d.String(), "DURABILITY_"))
}

// Request is a batch and how it must be delivered.
type Request struct {
	Records  []*record.Record
	Delivery Delivery
}

// Result says what the next hop took.
type Result struct {
	Accepted int
	Rejected []Rejection
	// Durability is how durable the batch is, as reported by the last hop that
	// took it. A wrapper never reports more than its successor did.
	Durability Durability
}

// Rejection is one record the next hop refused, and why.
type Rejection struct {
	ID     string
	Reason string
}

// Err returns an error naming the refusals, or nil.
func (r *Result) Err() error {
	if r == nil || len(r.Rejected) == 0 {
		return nil
	}
	parts := make([]string, 0, len(r.Rejected))
	for _, x := range r.Rejected {
		parts = append(parts, x.ID+": "+x.Reason)
	}
	return fmt.Errorf("sink refused %d of the batch: %s", len(r.Rejected), strings.Join(parts, "; "))
}

// Sink takes records. An implementation that cannot take them says so; it never
// reports success for a record it did not keep, because every guarantee above
// it is built on that answer being true.
type Sink interface {
	Write(ctx context.Context, req *Request) (*Result, error)
}

// Guarantor is implemented by a Sink that can say, before anything is written,
// the strongest durability it will ever report. A wrapper reports its
// successor's, and a sink that does not implement it guarantees nothing.
type Guarantor interface {
	Guarantees() Durability
}

// Guarantees returns the strongest durability s will report, or Unspecified
// when it does not say.
func Guarantees(s Sink) Durability {
	if g, ok := s.(Guarantor); ok {
		return g.Guarantees()
	}
	return Unspecified
}

// Require is the start-up guard behind `require:`: it refuses a chain that can
// never give min, so a misconfiguration fails when the process starts rather
// than on the first privileged action.
func Require(s Sink, least Durability) error {
	if s == nil {
		return errors.New("sink: no sink to require anything of")
	}
	if got := Guarantees(s); got < least {
		return fmt.Errorf("sink: this chain guarantees %s at best, and %s is required",
			durabilityName(got), durabilityName(least))
	}
	return nil
}

// Guard returns s refused at start-up by Require, and checked on every write:
// an acknowledgement weaker than least is an error, not a success with a caveat.
// An empty batch is not checked, because nothing was kept for it to be weak
// about.
func Guard(s Sink, least Durability) (Sink, error) {
	if err := Require(s, least); err != nil {
		return nil, err
	}
	return &guarded{next: s, least: least}, nil
}

type guarded struct {
	next  Sink
	least Durability
}

// Guarantees implements Guarantor.
func (g *guarded) Guarantees() Durability { return Guarantees(g.next) }

// Write implements Sink.
func (g *guarded) Write(ctx context.Context, req *Request) (*Result, error) {
	res, err := g.next.Write(ctx, req)
	if err != nil || len(req.Records) == 0 {
		return res, err
	}
	if res == nil || res.Durability < g.least {
		got := Unspecified
		if res != nil {
			got = res.Durability
		}
		return nil, fmt.Errorf("sink: the acknowledgement is %s and %s is required",
			durabilityName(got), durabilityName(g.least))
	}
	return res, nil
}

// Func adapts a function to a Sink.
type Func func(ctx context.Context, req *Request) (*Result, error)

// Write implements Sink.
func (f Func) Write(ctx context.Context, req *Request) (*Result, error) { return f(ctx, req) }

// Memory keeps records in memory. It is for tests, and for a deployment that
// has not been given a store yet, where it exists to make that obvious rather
// than to be useful. It reports Logged: the records are where a log line would
// be, in the process, and no stronger.
type Memory struct {
	// Fail, when set, is returned instead of accepting anything.
	Fail error
	// Limit caps how many records are kept; the oldest go first. Zero means no
	// limit.
	Limit int

	mu      sync.Mutex
	records []*record.Record
}

// Write implements Sink.
func (m *Memory) Write(_ context.Context, req *Request) (*Result, error) {
	if m.Fail != nil {
		return nil, m.Fail
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, req.Records...)
	if m.Limit > 0 && len(m.records) > m.Limit {
		m.records = m.records[len(m.records)-m.Limit:]
	}
	return &Result{Accepted: len(req.Records), Durability: Logged}, nil
}

// Guarantees implements Guarantor.
func (m *Memory) Guarantees() Durability { return Logged }

// Records returns what has been written, oldest first.
func (m *Memory) Records() []*record.Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*record.Record(nil), m.records...)
}

// Len is how many records are held.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.records)
}

// Reset forgets everything written.
func (m *Memory) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = nil
}

// Discard accepts everything and keeps nothing, so it reports Unspecified and
// satisfies no requirement.
var Discard Sink = Func(func(_ context.Context, req *Request) (*Result, error) {
	return &Result{Accepted: len(req.Records)}, nil
})
