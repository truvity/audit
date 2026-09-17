package writer

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/truvity/audit/record"
	"github.com/truvity/audit/store"
)

// DeadLetter is where a record the writer cannot process goes.
//
// Nothing is dropped. A record that names a catalogue version nobody
// registered, or that does not satisfy the schema it names, is a fault
// somewhere upstream, and the one thing that must not happen is for it to
// vanish while somebody works out which. It is kept, it is reported, and it can
// be replayed once the cause is fixed.
type DeadLetter interface {
	Put(ctx context.Context, r *record.Record, reason string) error
}

// StoreDeadLetter writes dead letters to the archive, under their own prefix.
type StoreDeadLetter struct {
	Store store.Store
	// RetainUntil is how long a dead letter is kept. A deployment gives it the
	// longest of its profiles, because until the record is understood nobody
	// knows which profile it belonged to.
	RetainUntil func(at time.Time) time.Time
	Instance    string
	Now         func() time.Time

	seq uint64
}

// Put implements DeadLetter.
func (d *StoreDeadLetter) Put(ctx context.Context, r *record.Record, reason string) error {
	now := time.Now().UTC()
	if d.Now != nil {
		now = d.Now().UTC()
	}
	line, err := record.Canonical(r)
	if err != nil {
		// A record that cannot even be encoded is kept as what little is known
		// of it, because the alternative is losing it entirely.
		line = []byte(fmt.Sprintf("{%q:%q}", "unencodable", err.Error()))
	}
	body, err := json.Marshal(struct {
		Reason string          `json:"reason"`
		At     string          `json:"at"`
		Writer string          `json:"writer"`
		ID     string          `json:"id"`
		Source string          `json:"source"`
		Action string          `json:"action"`
		Record json.RawMessage `json:"record"`
	}{
		Reason: reason, At: now.Format(time.RFC3339Nano), Writer: d.Instance,
		ID: r.GetId(), Source: r.GetSource(), Action: r.GetAction(), Record: line,
	})
	if err != nil {
		return fmt.Errorf("writer: dead letter: %w", err)
	}

	d.seq++
	key := fmt.Sprintf("dlq/year=%s/month=%s/day=%s/%019d-%s-%06d.json",
		now.Format("2006"), now.Format("01"), now.Format("02"),
		now.UnixNano(), d.Instance, d.seq)

	retain := now.AddDate(10, 0, 0)
	if d.RetainUntil != nil {
		retain = d.RetainUntil(now)
	}
	return d.Store.Put(ctx, store.Object{
		Key: key, Body: body, RetainUntil: retain,
		ContentType: "application/json",
		Metadata:    map[string]string{"audit-reason": reason, "audit-writer": d.Instance},
	})
}
