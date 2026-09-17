package writer

import (
	"context"
	"fmt"

	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/emit"
	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

// The writer's account of itself.
//
// A component that writes an audit trail and keeps no trail of its own asks to
// be taken on trust, and this one is not entitled to that: a reader who cannot
// tell a quiet hour from a stopped writer cannot conclude anything from the
// archive's silence. So the writer emits about itself, and it does so into
// itself, over the in-process sink. That is a loop by construction and it is
// the right one. The writer's account lands in the same archive, under the same
// profiles, in the same signed digests, and there is no second path to keep
// honest.
//
// Two rules keep the loop from turning on itself.
//
// Delivery is best-effort whatever the catalogue declares, because a blocking
// write from inside the writer's own batch would wait on its own flush, and
// because failing to record that a record was dead-lettered must not also fail
// the dead-lettering. The emitter is marked SelfReporting, which says this in
// the one place delivery is chosen.
//
// And a meta-record that cannot itself be written is dead-lettered and reported
// through the hooks, never emitted about. Without that, one malformed
// meta-record would beget another for as long as the process ran.

// metaKey marks a batch as the writer's own, which is how the two rules above
// are enforced: a dead letter under this marker is not emitted about, and the
// observer identity is the writer's own rather than a transport's.
type metaKey struct{}

func withMeta(ctx context.Context, instance string) context.Context {
	return context.WithValue(ctx, metaKey{}, instance)
}

// metaInstance reports whether this batch is the writer's account of itself,
// and under which instance name.
func metaInstance(ctx context.Context) (string, bool) {
	instance, ok := ctx.Value(metaKey{}).(string)
	return instance, ok
}

// startMeta builds the emitter the writer uses to speak about itself.
func (w *Writer) startMeta(c *catalogue.Catalogue) error {
	instance := w.Roller.Instance
	self, err := emit.New(emit.Options{
		Source:    c.Source,
		Catalogue: c,
		// Straight back into this writer, with the batch marked as its own.
		Sink: sink.Func(func(ctx context.Context, req *sink.Request) (*sink.Result, error) {
			return w.Write(withMeta(ctx, instance), req)
		}),
		SelfReporting: true,
		Version:       w.Version,
		Instance:      instance,
		Hooks: emit.Hooks{
			OnDropped: func(r *record.Record, reason string) {
				if w.Hooks.OnMetaDropped != nil {
					w.Hooks.OnMetaDropped(r.GetAction(), reason)
				}
			},
			OnRefused: func(r *record.Record, err error) {
				if w.Hooks.OnMetaDropped != nil {
					w.Hooks.OnMetaDropped(r.GetAction(), err.Error())
				}
			},
		},
	})
	if err != nil {
		return fmt.Errorf("writer: the writer's own emitter: %w", err)
	}
	w.self = self
	return nil
}

// meta returns a record of one of the writer's own actions, filled with what
// every one of them carries. The actor is the writer itself, as a machine
// identity: a record with no actor at all would be indistinguishable from one
// whose actor was stripped.
func (w *Writer) meta(action string, operation auditv1.Operation) *record.Record {
	return &record.Record{
		Action:    action,
		Operation: operation,
		TenantId:  record.TenantPlatform,
		Actor: &record.Actor{
			Kind: "system",
			Id:   w.Roller.Instance,
		},
	}
}

// emitMeta hands one of the writer's own records to its own emitter. It returns
// nothing: there is nothing the caller could usefully do, and the hooks already
// say what happened.
func (w *Writer) emitMeta(ctx context.Context, r *record.Record) {
	if w.self == nil {
		return
	}
	_ = w.self.Record(ctx, r)
}

// Started records that this writer instance began taking records. It is called
// once the writer is serving, not when it is constructed, so that the record
// means what a reader will take it to mean.
func (w *Writer) Started(ctx context.Context) {
	w.emitMeta(ctx, w.meta("audit.writer.started", auditv1.Operation_OPERATION_CREATE))
}

// Registered records that a catalogue version was registered with this writer.
// It is what lets a reader of the archive see when a source's description of
// itself changed, beside the copy of the catalogue the archive already keeps.
func (w *Writer) Registered(ctx context.Context, c *catalogue.Catalogue) {
	r := w.meta("audit.catalogue.registered", auditv1.Operation_OPERATION_CREATE)
	r.Targets = []*record.Target{{
		Type: "catalogue",
		Id:   c.Source + "@" + c.Version,
		Name: c.Source,
	}}
	w.emitMeta(ctx, r)
}

// metaDeadLettered records that a record could not be processed.
//
// It carries the dead-lettered record's identifier and the reason, and nothing
// of the record itself: the record is already in the dead-letter prefix, and
// copying a record that failed validation into a record that must pass it is
// how a writer refuses its own meta-events.
func (w *Writer) metaDeadLettered(ctx context.Context, failed *record.Record, reason string) {
	r := w.meta("audit.writer.dead_lettered", auditv1.Operation_OPERATION_CREATE)
	r.Outcome = &record.Outcome{
		Result: auditv1.Outcome_RESULT_FAILURE,
		Reason: reason,
		Code:   "dead_lettered",
	}
	id := failed.GetId()
	if id == "" {
		// A record with no identifier is exactly the kind that gets
		// dead-lettered, and the target is required.
		id = "unidentified"
	}
	r.Targets = []*record.Target{{Type: "record", Id: id}}
	w.emitMeta(ctx, r)
}

// stopped records that this writer instance is going away. Unlike the others it
// is delivered before the emitter closes, so that it is in the last batch
// rather than lost with it.
func (w *Writer) stopped(ctx context.Context) {
	w.emitMeta(ctx, w.meta("audit.writer.stopped", auditv1.Operation_OPERATION_REMOVE))
}
