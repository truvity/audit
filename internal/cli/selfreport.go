package cli

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/emit"
	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

// reporter is a scheduled job's account of itself.
//
// The writer keeps one so that a reader can tell a quiet hour from a stopped
// writer. The jobs need the same for the same reason, and more sharply: a chain
// that is never sealed and a chain sealed over an empty hour look identical in
// the archive, and a verification that never ran looks exactly like one that
// found nothing wrong. Without these events the only evidence that the jobs
// exist is that somebody is watching their exit codes.
//
// Every action goes through the common catalogue and is validated against it
// like anyone else's. A job whose own records bypassed the contract would be
// asking to be trusted about the one thing it exists to prove.
type reporter struct {
	emitter  *emit.Emitter
	instance string
}

// newReporter returns a reporter, or nil when there is nowhere to report to.
//
// A nil reporter records nothing and is the ordinary case for an operator
// running a command by hand: the point of these events is the scheduled run,
// and a person at a terminal can see the output.
func newReporter(c *catalogue.Catalogue, to sink.Sink, version, instance string) (*reporter, error) {
	if to == nil {
		return nil, nil
	}
	if c == nil {
		return nil, fmt.Errorf("a catalogue is required to record what this job did")
	}
	emitter, err := emit.New(emit.Options{
		Source:    c.Source,
		Catalogue: c,
		Sink:      to,
		// The job's account of itself is delivered best-effort whatever the
		// catalogue declares, because a job that blocked on recording that it
		// had worked could not report that it had not. See
		// emit.Options.SelfReporting.
		SelfReporting: true,
		Version:       version,
		Instance:      instance,
	})
	if err != nil {
		return nil, err
	}
	return &reporter{emitter: emitter, instance: instance}, nil
}

// close drains what is pending. A record that could not be delivered is already
// accounted for by the emitter's hooks.
func (r *reporter) close() {
	if r != nil && r.emitter != nil {
		_ = r.emitter.Close()
	}
}

// event is one of a job's own records, filled with what all of them carry.
//
// The actor is the job as a machine identity: a record with no actor at all
// cannot be told from one whose actor was stripped by a profile.
func (r *reporter) event(action, targetType, targetID string) *record.Record {
	return &record.Record{
		Action:   action,
		TenantId: record.TenantPlatform,
		Actor:    &record.Actor{Kind: "system", Id: r.instance},
		Targets:  []*record.Target{{Type: targetType, Id: targetID}},
	}
}

// record hands one event to the emitter. It returns nothing: a caller could do
// nothing useful with the error, and the failure of a job to describe itself
// must not become the failure of the job.
func (r *reporter) record(ctx context.Context, rec *record.Record) {
	if r == nil || r.emitter == nil {
		return
	}
	_ = r.emitter.Record(ctx, rec)
}

// succeeded marks an event as one that went well.
func succeeded(rec *record.Record, operation auditv1.Operation) *record.Record {
	rec.Operation = operation
	rec.Outcome = &record.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS}
	return rec
}

// failed marks an event as one that did not, with the reason a reader needs.
func failed(rec *record.Record, operation auditv1.Operation, reason string) *record.Record {
	rec.Operation = operation
	rec.Outcome = &record.Outcome{Result: auditv1.Outcome_RESULT_FAILURE, Reason: reason}
	return rec
}

// with attaches the action's extension data.
func with(rec *record.Record, data map[string]any) (*record.Record, error) {
	value, err := structpb.NewStruct(data)
	if err != nil {
		return nil, err
	}
	rec.Data = value
	return rec, nil
}

// confirm records an operator's act and waits until the writer has taken it.
//
// It is the opposite of a reporter. A reporter is a job's account of itself and
// is best-effort on purpose, because a job that blocked on saying it had worked
// could not say that it had not. An operator's act on the archive — destroying a
// key, placing or releasing a legal hold — is not an account of anything: it is
// the record the catalogue declares `block` for, and it is confirmed or it is an
// error the caller must surface.
//
// There is no "nowhere to record to" here. A command that confirms refuses to
// start without a writer, so a nil sink reaching this is a bug, not a choice.
func confirm(ctx context.Context, c *catalogue.Catalogue, to sink.Sink, rec *record.Record) error {
	if c == nil || to == nil {
		return fmt.Errorf("a writer and a catalogue are required to record %s", rec.GetAction())
	}
	emitter, err := emit.New(emit.Options{
		Source:    c.Source,
		Catalogue: c,
		Sink:      to,
		Instance:  record.InstanceName(),
	})
	if err != nil {
		return err
	}
	defer emitter.Close() //nolint:errcheck // the record is confirmed or returned as an error
	if rec.TenantId == "" {
		rec.TenantId = record.TenantPlatform
	}
	return emitter.Record(ctx, rec)
}
