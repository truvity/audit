// Package emit is what an application imports to record what it did.
//
// An emitter fills what only it knows, holds the record to the catalogue that
// describes it, and hands it to a sink under the delivery the catalogue
// declares for that action. It does not decide what a record means, where it is
// stored or how long it is kept: those belong to the catalogue, the writer and
// the profile.
//
// The one thing an emitter does decide is whether the caller waits. An action
// declared block does not complete until its record is durable, which is what
// makes a privileged or billable action fail rather than go unrecorded.
package emit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/truvity/audit/catalogue"
	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

// Hooks are where a deployment attaches its metrics and alerts. They are
// function fields rather than an interface so that this package, which every
// application imports, pulls in no telemetry library of its own.
type Hooks struct {
	// OnWritten is called after a batch is accepted.
	OnWritten func(n int, delivery sink.Delivery)
	// OnDropped is called for each record the async queue gave up.
	// A deployment that does not alert on this has no idea what it is missing.
	OnDropped func(r *record.Record, reason string)
	// OnRefused is called when a record does not satisfy its catalogue. This is
	// a fault in the emitting code, not a condition to tolerate.
	OnRefused func(r *record.Record, err error)
	// OnFailed is called when a sink refuses or fails a batch.
	OnFailed func(err error, delivery sink.Delivery, n int)
}

// Options configure an emitter.
type Options struct {
	// Source is the catalogue source this emitter speaks for.
	Source string
	// Catalogue describes the actions this emitter may record.
	Catalogue *catalogue.Catalogue
	// Sink is where records go.
	Sink sink.Sink

	// Version and Instance identify this process on every record it emits.
	// The writer replaces the observer's identity with the one it verified;
	// these two are the emitter's own account of itself.
	Version  string
	Instance string

	// Bounds default to record.Default.
	Bounds record.Bounds
	// Timeout bounds a blocking write. Default 10s.
	Timeout time.Duration
	// SelfReporting marks an emitter whose sink is the component it emits
	// about: the writer's account of itself, and the scheduled jobs' accounts
	// of themselves. Every action it records is delivered async, whatever
	// the catalogue declares, and the deployment alerts on OnDropped.
	//
	// This is the one place a declared delivery is not honoured, and the reason
	// is that honouring it would be incoherent rather than merely slow. A
	// blocking write from inside the component performing the write waits on
	// its own batch, and a record that cannot be made durable would fail the
	// very work whose failure it is reporting.
	//
	// An application emitting about work it does for somebody else must leave
	// this false. For those, an action declared block and not recorded is meant
	// to fail: that is what makes a privileged or billable action fail rather
	// than go unrecorded.
	SelfReporting bool

	// Queue is how many async records may wait to be acknowledged. Default
	// 1024. It is the whole of what a dying pod loses: a record leaves it only
	// once the sink has said it is durable. When it is full the oldest is
	// dropped, counted and reported to OnDropped, on the reasoning that the
	// newest record is the one somebody is still able to act on.
	Queue int
	// Batch and Flush are how async records are grouped. Defaults 100 and one
	// second. Flush is also, in practice, how long a record sits in the queue
	// before anything tries to deliver it.
	Batch int
	Flush time.Duration
	// Retry is how long the emitter waits before trying a batch the sink could
	// not take, doubling up to a minute. Default one second.
	Retry time.Duration

	Hooks Hooks
}

// Emitter records what an application did.
type Emitter struct {
	source    string
	catalogue *catalogue.Catalogue
	sink      sink.Sink
	version   string
	instance  string
	bounds    record.Bounds
	timeout   time.Duration
	hooks     Hooks
	retry     time.Duration
	self      bool

	seq record.Sequencer

	queue   chan *record.Record
	pending atomic.Int64
	batch   int
	flush   time.Duration
	done    chan struct{}
	stopped sync.Once
	wg      sync.WaitGroup
}

// ErrRefused is returned when a record does not satisfy its catalogue. It
// wraps the reasons.
var ErrRefused = errors.New("emit: the record does not satisfy its catalogue")

// New returns an emitter.
//
// It refuses at once if the catalogue declares a delivery this component no
// longer has, rather than discovering it at the moment that record matters. An
// emitter that quietly downgrades a delivery is worse than one that will not
// start.
func New(o Options) (*Emitter, error) {
	switch {
	case o.Source == "":
		return nil, errors.New("emit: a source is required")
	case o.Catalogue == nil:
		return nil, errors.New("emit: a catalogue is required: a record must name what describes it")
	case o.Catalogue.Source != o.Source:
		return nil, fmt.Errorf("emit: source %q was given the catalogue of %q", o.Source, o.Catalogue.Source)
	case o.Sink == nil:
		return nil, errors.New("emit: a sink is required")
	}
	for _, name := range o.Catalogue.ActionNames() {
		a, _ := o.Catalogue.Action(name)
		if _, err := sink.ParseDelivery(a.Delivery); err != nil {
			return nil, fmt.Errorf("emit: action %s: %w", name, err)
		}
	}

	e := &Emitter{
		source:    o.Source,
		catalogue: o.Catalogue,
		sink:      o.Sink,
		version:   o.Version,
		instance:  o.Instance,
		bounds:    o.Bounds,
		timeout:   o.Timeout,
		hooks:     o.Hooks,
		batch:     o.Batch,
		flush:     o.Flush,
		retry:     o.Retry,
		self:      o.SelfReporting,
		done:      make(chan struct{}),
	}
	if e.instance == "" {
		e.instance = record.InstanceName()
	}
	if e.bounds == (record.Bounds{}) {
		e.bounds = record.Default
	}
	if e.timeout <= 0 {
		e.timeout = 10 * time.Second
	}
	if e.batch <= 0 {
		e.batch = 100
	}
	if e.flush <= 0 {
		e.flush = time.Second
	}
	queue := o.Queue
	if queue <= 0 {
		queue = 1024
	}
	if e.retry <= 0 {
		e.retry = time.Second
	}
	e.queue = make(chan *record.Record, queue)
	e.wg.Add(1)
	go e.run()
	return e, nil
}

// Record records one thing that happened.
//
// It fills the identifier, the times, the versions and the sequence, holds the
// record to its catalogue, and delivers it as the action declares. It returns
// an error when the record is not one this catalogue describes, and when a
// blocking delivery did not become durable. Under async delivery it returns as
// soon as the record is queued — and nil even if the queue later overflows and
// gives it up, because the caller has nothing useful to do about that and the
// hooks do.
//
// The record passed in is filled in place.
func (e *Emitter) Record(ctx context.Context, r *record.Record) error {
	if r == nil {
		return errors.New("emit: nil record")
	}
	if r.GetSource() == "" {
		r.Source = e.source
	}
	if r.GetCatalogueVersion() == "" {
		r.CatalogueVersion = e.catalogue.Version
	}
	if r.GetObserver() == nil {
		r.Observer = &record.Observer{}
	}
	if r.GetObserver().GetVersion() == "" {
		r.Observer.Version = e.version
	}
	if r.GetObserver().GetInstance() == "" {
		r.Observer.Instance = e.instance
	}
	// The catalogue says what kind of operation an action is; a caller that
	// says nothing takes it from there, and one that says otherwise is
	// refused by the validation below.
	if r.GetOperation() == auditv1.Operation_OPERATION_UNSPECIFIED {
		if a, ok := e.catalogue.Action(r.GetAction()); ok {
			if v, ok := auditv1.Operation_value["OPERATION_"+strings.ToUpper(a.Operation)]; ok {
				r.Operation = auditv1.Operation(v)
			}
		}
	}
	record.Assign(r)
	if r.GetSequence() == 0 {
		r.Sequence = e.seq.Next()
	}
	applyRequestContext(ctx, r)
	record.Normalise(r, e.bounds)

	if err := e.validate(r); err != nil {
		if e.hooks.OnRefused != nil {
			e.hooks.OnRefused(r, err)
		}
		return err
	}

	delivery := sink.Async
	if a, ok := e.catalogue.Action(r.GetAction()); ok && !e.self {
		if d, err := sink.ParseDelivery(a.Delivery); err == nil {
			delivery = d
		}
	}
	if delivery == sink.Block {
		return e.writeNow(ctx, r)
	}
	e.enqueue(r)
	return nil
}

// validate holds a record to the core rules and then to its catalogue.
func (e *Emitter) validate(r *record.Record) error {
	var problems []error
	if err := record.Check(r, e.bounds); err != nil {
		problems = append(problems, err)
	}
	x, err := e.catalogue.Compose(r.GetAction())
	if err != nil {
		problems = append(problems, err)
	} else if err := x.Validate(r); err != nil {
		problems = append(problems, err)
	}
	if err := errors.Join(problems...); err != nil {
		return fmt.Errorf("%w: %w", ErrRefused, err)
	}
	return nil
}

// writeNow delivers under a context of its own, so that a request whose client
// has gone away still records what it did.
func (e *Emitter) writeNow(ctx context.Context, records ...*record.Record) error {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.timeout)
	defer cancel()
	res, err := e.sink.Write(writeCtx, &sink.Request{Records: records, Delivery: sink.Block})
	if err == nil {
		err = res.Err()
	}
	if err != nil {
		if e.hooks.OnFailed != nil {
			e.hooks.OnFailed(err, sink.Block, len(records))
		}
		return fmt.Errorf("emit: the record did not become durable: %w", err)
	}
	if e.hooks.OnWritten != nil {
		e.hooks.OnWritten(len(records), sink.Block)
	}
	return nil
}

// enqueue hands a record to the background writer. A full queue means the sink
// has been unreachable for longer than the queue is deep: the oldest record is
// given up, loudly, so that the newest — the one somebody may still be able to
// act on — is kept.
func (e *Emitter) enqueue(r *record.Record) {
	for {
		select {
		case e.queue <- r:
			e.pending.Add(1)
			return
		default:
		}
		select {
		case oldest := <-e.queue:
			e.pending.Add(-1)
			if e.hooks.OnDropped != nil {
				e.hooks.OnDropped(oldest, "the queue is full")
			}
		default:
			// Another goroutine emptied it in between. Try to add again.
		}
	}
}

// run batches async records and delivers them, retrying until the sink takes
// them. A record leaves this process only once the sink has said it is
// durable, so what a dying pod loses is exactly what is still in here.
func (e *Emitter) run() {
	defer e.wg.Done()
	ticker := time.NewTicker(e.flush)
	defer ticker.Stop()
	pending := make([]*record.Record, 0, e.batch)

	send := func(final bool) {
		if len(pending) == 0 {
			return
		}
		batch := pending
		pending = make([]*record.Record, 0, e.batch)
		delivered := e.deliver(batch, final)
		// Either way the records have left this emitter: delivered, or given
		// up and said to be.
		e.pending.Add(-int64(len(batch)))
		if delivered {
			return
		}
		// Shutting down with a batch the sink would not take. It is lost, and
		// said to be: the records are in the application's log either way.
		if e.hooks.OnDropped != nil {
			for _, r := range batch {
				e.hooks.OnDropped(r, "the emitter stopped before the sink took it")
			}
		}
	}

	for {
		select {
		case r := <-e.queue:
			pending = append(pending, r)
			if len(pending) >= e.batch {
				send(false)
			}
		case <-ticker.C:
			send(false)
		case <-e.done:
			// Drain what is already queued before going.
			for {
				select {
				case r := <-e.queue:
					pending = append(pending, r)
					if len(pending) >= e.batch {
						send(true)
					}
					continue
				default:
				}
				break
			}
			send(true)
			return
		}
	}
}

// deliver hands a batch to the sink, retrying with backoff until it is taken.
// It reports whether the batch became durable.
//
// A sink that answers is a sink that has decided: a batch it refuses as
// invalid is dead-lettered where it was refused, never retried, because
// nothing about sending it again would make it valid. What is retried is a
// sink that could not be reached or could not answer — the case the queue
// exists for.
//
// Retrying is unbounded while the emitter is running, because a queue that
// gives up is the thing async delivery was changed to stop being. Once the
// emitter is stopping it is bounded by one timeout: a process on its way out
// should try, and should not hold the exit open on a sink that is down.
func (e *Emitter) deliver(batch []*record.Record, final bool) bool {
	wait := e.retry
	var deadline time.Time
	if final {
		deadline = time.Now().Add(e.timeout)
	}
	for {
		if ok, decided := e.attempt(batch); decided {
			return ok
		}
		if deadline.IsZero() {
			select {
			case <-time.After(wait):
			case <-e.done:
				// Stopping. Keep trying, but not forever.
				deadline = time.Now().Add(e.timeout)
			}
		} else {
			left := time.Until(deadline)
			if left <= 0 {
				return false
			}
			time.Sleep(min(wait, left))
		}
		if wait < maxRetryWait {
			wait *= 2
		}
	}
}

// attempt makes one call. It reports whether the batch is durable, and whether
// the sink decided at all — a sink that answered will answer the same way to
// the same batch, so there is nothing to retry.
func (e *Emitter) attempt(batch []*record.Record) (durable, decided bool) {
	ctx, cancel := context.WithTimeout(context.Background(), e.timeout)
	defer cancel()
	res, err := e.sink.Write(ctx, &sink.Request{Records: batch, Delivery: sink.Async})
	if err != nil {
		if e.hooks.OnFailed != nil {
			e.hooks.OnFailed(err, sink.Async, len(batch))
		}
		return false, false
	}
	if refused := res.Err(); refused != nil {
		// The sink took the batch and refused records in it. That refusal is
		// recorded where it happened; retrying would only repeat it.
		if e.hooks.OnFailed != nil {
			e.hooks.OnFailed(refused, sink.Async, len(batch))
		}
		return true, true
	}
	if e.hooks.OnWritten != nil {
		e.hooks.OnWritten(len(batch), sink.Async)
	}
	return true, true
}

// maxRetryWait caps the backoff. A sink that has been gone this long is an
// incident somebody is looking at, and waiting longer between tries only makes
// the queue overflow sooner once it comes back.
const maxRetryWait = time.Minute

// Pending is how many records are waiting to be delivered. It is what this
// process would lose if it stopped now, and what InstrumentQueue publishes.
func (e *Emitter) Pending() int {
	return int(e.pending.Load())
}

// Close stops the emitter and delivers what is still queued, with one last
// attempt per batch. A process that exits without calling it loses whatever
// was still queued, which is what async delivery costs and why an action that
// may not go unrecorded is declared block.
func (e *Emitter) Close() error {
	e.stopped.Do(func() { close(e.done) })
	e.wg.Wait()
	return nil
}
