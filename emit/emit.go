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
	"sync"
	"time"

	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

// Hooks are where a deployment attaches its metrics and alerts. They are
// function fields rather than an interface so that this package, which every
// application imports, pulls in no telemetry library of its own.
type Hooks struct {
	// OnWritten is called after a batch is accepted.
	OnWritten func(n int, delivery sink.Delivery)
	// OnDropped is called for each record given up under best-effort delivery.
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
	// Outbox, when set, makes outbox delivery available. Without one, an
	// emitter refuses a catalogue that declares it.
	Outbox Outbox
	// Publish is how often the outbox is drained. Default one second.
	Publish time.Duration

	// Queue is how many best-effort records may wait. Default 1024.
	Queue int
	// Batch and Flush are how best-effort records are grouped. Defaults 100
	// and one second.
	Batch int
	Flush time.Duration

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
	outbox    Outbox
	publish   time.Duration

	seq record.Sequencer

	queue   chan *record.Record
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
// It refuses at once if the catalogue declares an action this emitter could not
// deliver as declared, rather than discovering it at the moment that record
// matters. An emitter that quietly downgrades a delivery mode is worse than one
// that will not start.
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
		d, err := sink.ParseDelivery(a.Delivery)
		if err != nil {
			return nil, fmt.Errorf("emit: action %s: %w", name, err)
		}
		if d == sink.Outbox && o.Outbox == nil {
			return nil, fmt.Errorf(
				"emit: action %s declares outbox delivery and no outbox is configured; "+
					"give one, or declare block or best_effort", name)
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
		outbox:    o.Outbox,
		publish:   o.Publish,
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
	if e.publish <= 0 {
		e.publish = time.Second
	}
	e.queue = make(chan *record.Record, queue)
	e.wg.Add(1)
	go e.run()
	if e.outbox != nil {
		e.wg.Add(1)
		go e.publisher()
	}
	return e, nil
}

// Record records one thing that happened.
//
// It fills the identifier, the times, the versions and the sequence, holds the
// record to its catalogue, and delivers it as the action declares. It returns
// an error when the record is not one this catalogue describes, and when a
// blocking delivery did not become durable. Under best-effort delivery it
// returns nil even if the record is later given up, because the caller has
// nothing useful to do about that and the hooks do.
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

	delivery := sink.BestEffort
	if a, ok := e.catalogue.Action(r.GetAction()); ok {
		if d, err := sink.ParseDelivery(a.Delivery); err == nil {
			delivery = d
		}
	}
	switch delivery {
	case sink.Block:
		return e.writeNow(ctx, r)
	case sink.Outbox:
		// The record is on the disk before the request completes. What is left
		// is a delay, not a loss.
		if err := e.outbox.Append(ctx, r); err != nil {
			if e.hooks.OnFailed != nil {
				e.hooks.OnFailed(err, sink.Outbox, 1)
			}
			return fmt.Errorf("emit: the record did not reach the outbox: %w", err)
		}
		return nil
	default:
		return e.enqueue(r)
	}
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

// enqueue hands a record to the background writer, or gives it up loudly.
func (e *Emitter) enqueue(r *record.Record) error {
	select {
	case e.queue <- r:
		return nil
	default:
		if e.hooks.OnDropped != nil {
			e.hooks.OnDropped(r, "the queue is full")
		}
		return nil
	}
}

// run batches best-effort records and writes them.
func (e *Emitter) run() {
	defer e.wg.Done()
	ticker := time.NewTicker(e.flush)
	defer ticker.Stop()
	pending := make([]*record.Record, 0, e.batch)

	send := func() {
		if len(pending) == 0 {
			return
		}
		batch := pending
		pending = make([]*record.Record, 0, e.batch)
		ctx, cancel := context.WithTimeout(context.Background(), e.timeout)
		defer cancel()
		res, err := e.sink.Write(ctx, &sink.Request{Records: batch, Delivery: sink.BestEffort})
		if err == nil {
			err = res.Err()
		}
		if err != nil {
			if e.hooks.OnFailed != nil {
				e.hooks.OnFailed(err, sink.BestEffort, len(batch))
			}
			// Best effort is best effort: a failed batch is given up rather
			// than retried into a queue that is already behind. What must not
			// be given up is not declared best effort.
			if e.hooks.OnDropped != nil {
				for _, r := range batch {
					e.hooks.OnDropped(r, "the sink refused the batch: "+err.Error())
				}
			}
			return
		}
		if e.hooks.OnWritten != nil {
			e.hooks.OnWritten(len(batch), sink.BestEffort)
		}
	}

	for {
		select {
		case r := <-e.queue:
			pending = append(pending, r)
			if len(pending) >= e.batch {
				send()
			}
		case <-ticker.C:
			send()
		case <-e.done:
			// Drain what is already queued before going.
			for {
				select {
				case r := <-e.queue:
					pending = append(pending, r)
					if len(pending) >= e.batch {
						send()
					}
					continue
				default:
				}
				break
			}
			send()
			return
		}
	}
}

// publisher drains the outbox until the emitter stops.
func (e *Emitter) publisher() {
	defer e.wg.Done()
	ticker := time.NewTicker(e.publish)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			e.drain()
		case <-e.done:
			// One last pass, so a clean shutdown delivers what it can. What it
			// cannot deliver stays on the disk for the next process.
			e.drain()
			return
		}
	}
}

// drain hands the outbox's batches to the sink. A failure is left for the next
// pass: the records are on the disk, which is the whole point of them being
// there.
func (e *Emitter) drain() {
	ctx, cancel := context.WithTimeout(context.Background(), e.timeout)
	defer cancel()
	err := e.outbox.Deliver(ctx, func(ctx context.Context, batch []*record.Record) error {
		res, err := e.sink.Write(ctx, &sink.Request{Records: batch, Delivery: sink.Outbox})
		if err == nil {
			err = res.Err()
		}
		if err != nil {
			return err
		}
		if e.hooks.OnWritten != nil {
			e.hooks.OnWritten(len(batch), sink.Outbox)
		}
		return nil
	})
	if err != nil && e.hooks.OnFailed != nil {
		e.hooks.OnFailed(err, sink.Outbox, 0)
	}
}

// Close stops the emitter and writes what is still queued. A process that exits
// without calling it loses whatever had not been flushed, which is the
// difference between best-effort and the other two modes.
func (e *Emitter) Close() error {
	e.stopped.Do(func() { close(e.done) })
	e.wg.Wait()
	if e.outbox != nil {
		return e.outbox.Close()
	}
	return nil
}
