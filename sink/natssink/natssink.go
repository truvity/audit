// Package natssink publishes records to a NATS JetStream stream, and consumes
// them again on the other side.
//
// The stream is a buffer with a bounded horizon, not a store: the writer's put
// into object storage is where durability begins. What the stream gives is a
// business-visible acknowledgement — a publish that returns means the record is
// on the stream's disks and will be delivered — and replay for as long as the
// horizon holds.
package natssink

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

// Publisher is a Sink that puts records on a stream.
type Publisher struct {
	js      jetstream.JetStream
	subject string
	timeout time.Duration
}

// Options configure a publisher.
type Options struct {
	// Subject records are published to.
	Subject string
	// Timeout bounds one publish. Default 5s.
	Timeout time.Duration
}

// NewPublisher returns a Sink publishing to a JetStream subject.
func NewPublisher(js jetstream.JetStream, o Options) (*Publisher, error) {
	if js == nil {
		return nil, errors.New("natssink: a JetStream context is required")
	}
	if o.Subject == "" {
		return nil, errors.New("natssink: a subject is required")
	}
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Second
	}
	return &Publisher{js: js, subject: o.Subject, timeout: o.Timeout}, nil
}

// Write implements sink.Sink.
//
// Every record is published under its own identifier as the message id, so the
// stream's own duplicate window absorbs a retry: a publisher that did not see
// an acknowledgement may safely send again, which is what makes the queue's retries and
// the emitter's retries harmless.
//
// It waits for every acknowledgement before returning, whatever the delivery
// mode. Reporting success for a record the stream has not taken would break the
// one guarantee the caller has, and async records reach here in batches
// already, so there is nothing to gain by not waiting.
func (p *Publisher) Write(ctx context.Context, req *sink.Request) (*sink.Result, error) {
	if len(req.Records) == 0 {
		return &sink.Result{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	futures := make([]jetstream.PubAckFuture, 0, len(req.Records))
	result := &sink.Result{}
	for _, r := range req.Records {
		line, err := record.Canonical(r)
		if err != nil {
			result.Rejected = append(result.Rejected, sink.Rejection{
				ID: r.GetId(), Reason: "cannot be encoded: " + err.Error(),
			})
			continue
		}
		f, err := p.js.PublishMsgAsync(&nats.Msg{
			Subject: p.subject,
			Data:    line,
			Header: nats.Header{
				"Nats-Msg-Id": []string{r.GetId()},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("natssink: publish: %w", err)
		}
		futures = append(futures, f)
	}

	for _, f := range futures {
		select {
		case <-f.Ok():
			result.Accepted++
		case err := <-f.Err():
			return nil, fmt.Errorf("natssink: the stream did not take the record: %w", err)
		case <-ctx.Done():
			return nil, fmt.Errorf("natssink: waiting for the stream: %w", ctx.Err())
		}
	}
	return result, nil
}

// Consumer reads records from a stream and hands them to a Sink, which on the
// other side of a deployment is the split writer.
type Consumer struct {
	consumer   jetstream.Consumer
	target     sink.Sink
	batch      int
	window     time.Duration
	maxRecords int
	maxBytes   int
	onError    func(error)
	now        func() time.Time
}

// ConsumerOptions configure a consumer.
type ConsumerOptions struct {
	// Batch is how many records are fetched from the stream at once.
	// Default 100.
	Batch int

	// Window, MaxRecords and MaxBytes are the roll: how much a consumer
	// gathers from the stream before handing it to the target, which is what
	// decides how many objects a day of records becomes.
	//
	// Fetching from a stream returns whatever is there, which under a light
	// load is a handful of records a second. Writing each fetch straight
	// through would make an object of each, and an archive of many tiny
	// objects costs a request to put, a line in every hour's digest and an
	// entry in every listing, forever. So the consumer gathers until one of
	// the three is reached and writes once.
	//
	// Nothing is lost by waiting: the messages stay unacknowledged until the
	// target has taken them, so a consumer that dies mid-window leaves them on
	// the stream for the next one.
	//
	// Defaults: 30 seconds, 5000 records, 8 MiB.
	Window     time.Duration
	MaxRecords int
	MaxBytes   int

	// AckWait is the stream's, and is checked rather than used: a window that
	// outlasts it would have the stream redeliver records the consumer is
	// still holding, which turns one object into two and a quiet deployment
	// into a loop. Zero skips the check.
	AckWait time.Duration

	// OnError is called for a batch the target refused. The message is not
	// acknowledged, so the stream redelivers it; without this a deployment
	// would watch records go round silently.
	OnError func(error)
	// Now is the clock, for tests. Default time.Now.
	Now func() time.Time
}

// NewConsumer binds a consumer to a Sink.
func NewConsumer(c jetstream.Consumer, target sink.Sink, o ConsumerOptions) (*Consumer, error) {
	if c == nil {
		return nil, errors.New("natssink: a consumer is required")
	}
	if target == nil {
		return nil, errors.New("natssink: a target sink is required")
	}
	if o.Batch <= 0 {
		o.Batch = 100
	}
	if o.Window <= 0 {
		o.Window = 30 * time.Second
	}
	if o.MaxRecords <= 0 {
		o.MaxRecords = 5000
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = 8 << 20
	}
	if o.AckWait > 0 && o.AckWait <= o.Window {
		return nil, fmt.Errorf(
			"natssink: the stream's ack wait (%s) is not longer than the roll window (%s), so the "+
				"stream would redeliver records this consumer is still gathering, and write them "+
				"twice; raise the ack wait or shorten the window", o.AckWait, o.Window)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Consumer{
		consumer: c, target: target, batch: o.Batch,
		window: o.Window, maxRecords: o.MaxRecords, maxBytes: o.MaxBytes,
		onError: o.OnError, now: o.Now,
	}, nil
}

// Run consumes until the context is cancelled.
//
// Records are gathered across fetches and written once a roll condition is
// reached, and the messages that carried them are acknowledged only after the
// target has taken them. A target that fails leaves them unacknowledged, so the
// stream redelivers and nothing is lost to a writer that was briefly unable to
// write. The same is true of the gathering itself: a consumer that dies with a
// window half full has acknowledged none of it.
func (c *Consumer) Run(ctx context.Context) error {
	var held batchInProgress
	for {
		if err := ctx.Err(); err != nil {
			// What was gathered and not written is left unacknowledged
			// deliberately: this process is going, and the stream is where the
			// records still are.
			return nil //nolint:nilerr // a cancelled run is not a failure
		}
		batch, err := c.consumer.Fetch(c.batch, jetstream.FetchMaxWait(c.waitFor(&held)))
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, nats.ErrTimeout) {
				// A quiet stream still has to close a window that is open, or
				// the last records of a slow day would wait for the next one.
				c.rollIfDue(ctx, &held)
				continue
			}
			return fmt.Errorf("natssink: fetch: %w", err)
		}

		for msg := range batch.Messages() {
			var r record.Record
			if err := record.Unmarshal(msg.Data(), &r); err != nil {
				// A message this build cannot decode will never decode. Leaving
				// it unacknowledged would wedge the stream behind it, so it is
				// acknowledged and reported; a dead letter is the writer's to
				// keep, and it is the one that has a bucket.
				c.report(fmt.Errorf("natssink: a message could not be decoded and was skipped: %w", err))
				_ = msg.Ack()
				continue
			}
			held.add(msg, &r, c.now())
			if c.full(&held) {
				c.roll(ctx, &held)
			}
		}
		if err := batch.Error(); err != nil {
			c.report(fmt.Errorf("natssink: batch: %w", err))
		}
		c.rollIfDue(ctx, &held)
	}
}

// waitFor is how long to block on the next fetch.
//
// A quiet stream would otherwise hold the fetch open past the window, and the
// records already gathered would be written late — late enough, if the stream's
// ack wait is short, for it to offer them to another writer meanwhile and have
// them written twice. So while a batch is open the fetch waits only as long as
// the window has left.
func (c *Consumer) waitFor(b *batchInProgress) time.Duration {
	const idle = time.Second
	if len(b.records) == 0 {
		return idle
	}
	left := c.window - c.now().Sub(b.opened)
	switch {
	case left <= 0:
		// Due now: ask for whatever is there and go and write.
		return time.Millisecond
	case left < idle:
		return left
	default:
		return idle
	}
}

// batchInProgress is what a consumer has taken from the stream and not yet
// handed on: the records, the messages that carried them so they can be
// acknowledged together, and when the first of them arrived.
type batchInProgress struct {
	messages []jetstream.Msg
	records  []*record.Record
	bytes    int
	opened   time.Time
}

func (b *batchInProgress) add(msg jetstream.Msg, r *record.Record, at time.Time) {
	if len(b.records) == 0 {
		b.opened = at
	}
	b.messages = append(b.messages, msg)
	b.records = append(b.records, r)
	b.bytes += len(msg.Data())
}

func (b *batchInProgress) reset() {
	*b = batchInProgress{}
}

// full reports whether what is held has reached a size worth writing.
func (c *Consumer) full(b *batchInProgress) bool {
	return len(b.records) >= c.maxRecords || b.bytes >= c.maxBytes
}

// rollIfDue writes what is held when its window has run out.
func (c *Consumer) rollIfDue(ctx context.Context, b *batchInProgress) {
	if len(b.records) == 0 {
		return
	}
	if c.full(b) || c.now().Sub(b.opened) >= c.window {
		c.roll(ctx, b)
	}
}

// roll hands what is held to the target and acknowledges it.
//
// A failure gives the batch up rather than retrying it in this process. The
// messages were never acknowledged, so the stream redelivers them, and the
// stream is the thing that knows how to retry: it has the ack wait, the
// delivery count and the backoff. Holding them here instead would keep them
// past the ack wait, at which point the stream redelivers anyway and the
// consumer finds itself holding two copies of a record it has not written
// once.
func (c *Consumer) roll(ctx context.Context, b *batchInProgress) {
	if len(b.records) == 0 {
		return
	}
	res, err := c.target.Write(ctx, &sink.Request{Records: b.records, Delivery: sink.Block})
	if err == nil {
		err = res.Err()
	}
	if err != nil {
		c.report(err)
		b.reset()
		return
	}
	for _, msg := range b.messages {
		if err := msg.Ack(); err != nil {
			c.report(fmt.Errorf("natssink: acknowledge: %w", err))
		}
	}
	b.reset()
}

func (c *Consumer) report(err error) {
	if c.onError != nil {
		c.onError(err)
	}
}
