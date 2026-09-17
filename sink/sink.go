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
	// Outbox writes to a durable local store and publishes later. The request
	// completes at once and nothing is lost to a restart, at the cost of the
	// record arriving late.
	Outbox = auditv1.Delivery_DELIVERY_OUTBOX
	// BestEffort may drop under pressure, loudly. It is for records whose loss
	// is an operational problem rather than a compliance one.
	BestEffort = auditv1.Delivery_DELIVERY_BEST_EFFORT
)

// ParseDelivery reads the spelling a catalogue uses.
func ParseDelivery(s string) (Delivery, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "block":
		return Block, nil
	case "outbox":
		return Outbox, nil
	case "best_effort", "best-effort", "":
		return BestEffort, nil
	default:
		return auditv1.Delivery_DELIVERY_UNSPECIFIED, fmt.Errorf("sink: %q is not a delivery mode", s)
	}
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

// Func adapts a function to a Sink.
type Func func(ctx context.Context, req *Request) (*Result, error)

// Write implements Sink.
func (f Func) Write(ctx context.Context, req *Request) (*Result, error) { return f(ctx, req) }

// Memory keeps records in memory. It is for tests, and for a deployment that
// has not been given a store yet, where it exists to make that obvious rather
// than to be useful.
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
	return &Result{Accepted: len(req.Records)}, nil
}

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

// Discard accepts everything and keeps nothing.
var Discard Sink = Func(func(_ context.Context, req *Request) (*Result, error) {
	return &Result{Accepted: len(req.Records)}, nil
})
