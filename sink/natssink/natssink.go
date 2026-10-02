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

// Guarantees implements sink.Guarantor.
func (p *Publisher) Guarantees() sink.Durability { return sink.Queued }

// Write implements sink.Sink. It reports Queued: a publish is acknowledged only
// once the stream has replicated it.
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
//
// A reconnect is not a refusal. The client fails every acknowledgement still
// outstanding when its connection drops, and a connection bound to a token that
// expires drops on a schedule, so a record in flight at that moment would fail
// its caller for no reason of its own: an async record is only delayed by the
// emitter's retry, but a block record fails the action it was recording. Such
// records are published again, within the same timeout, once the connection is
// back. The message id makes that harmless: a record whose acknowledgement was
// lost rather than its publish is absorbed by the duplicate window.
func (p *Publisher) Write(ctx context.Context, req *sink.Request) (*sink.Result, error) {
	if len(req.Records) == 0 {
		return &sink.Result{Durability: sink.Queued}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	result := &sink.Result{Durability: sink.Queued}
	pending := make([]outgoing, 0, len(req.Records))
	for _, r := range req.Records {
		line, err := record.Canonical(r)
		if err != nil {
			result.Rejected = append(result.Rejected, sink.Rejection{
				ID: r.GetId(), Reason: "cannot be encoded: " + err.Error(),
			})
			continue
		}
		pending = append(pending, outgoing{id: r.GetId(), data: line})
	}

	pause := retryPause
	for len(pending) > 0 {
		again, cause, err := p.publish(ctx, pending, result)
		if err != nil {
			return nil, err
		}
		if len(again) == 0 {
			break
		}
		if err := p.awaitConnection(ctx, pause); err != nil {
			return nil, fmt.Errorf("natssink: the connection did not come back in time to publish "+
				"again after %v: %w", cause, err)
		}
		pause = min(pause*2, maxRetryPause)
		pending = again
	}
	return result, nil
}

// outgoing is a record on its way to the stream. A message is built afresh for
// every attempt, because the client writes its reply subject into the one it
// is handed and refuses to publish a message that already carries one.
type outgoing struct {
	id   string
	data []byte
}

func (o outgoing) msg(subject string) *nats.Msg {
	return &nats.Msg{
		Subject: subject,
		Data:    o.data,
		Header:  nats.Header{"Nats-Msg-Id": []string{o.id}},
	}
}

// publish sends one round and waits for its acknowledgements. It returns the
// records to send again, because the connection dropped under them, and the
// error that said so; any other failure ends the write.
func (p *Publisher) publish(ctx context.Context, round []outgoing, result *sink.Result) ([]outgoing, error, error) {
	var (
		again   []outgoing
		cause   error
		sent    = make([]outgoing, 0, len(round))
		futures = make([]jetstream.PubAckFuture, 0, len(round))
	)
	for _, o := range round {
		f, err := p.js.PublishMsgAsync(o.msg(p.subject))
		if err != nil {
			if !reconnecting(err) {
				return nil, nil, fmt.Errorf("natssink: publish: %w", err)
			}
			again, cause = append(again, o), err
			continue
		}
		sent = append(sent, o)
		futures = append(futures, f)
	}

	for i, f := range futures {
		select {
		case <-f.Ok():
			result.Accepted++
		case err := <-f.Err():
			if !reconnecting(err) {
				return nil, nil, fmt.Errorf("natssink: the stream did not take the record: %w", err)
			}
			again, cause = append(again, sent[i]), err
		case <-ctx.Done():
			return nil, nil, fmt.Errorf("natssink: waiting for the stream: %w", ctx.Err())
		}
	}
	return again, cause, nil
}

// reconnecting reports whether a publish failed because the connection went
// away underneath it, rather than because the stream refused the record. Only
// those are worth sending again: a refusal — a full stream, a subject no stream
// listens on — will be the same refusal a moment later.
func reconnecting(err error) bool {
	return errors.Is(err, nats.ErrDisconnected) ||
		errors.Is(err, nats.ErrConnectionReconnecting) ||
		errors.Is(err, nats.ErrNoResponders) ||
		errors.Is(err, jetstream.ErrNoStreamResponse)
}

// The pause before publishing again doubles from retryPause to maxRetryPause.
// A reconnect to a live broker takes milliseconds; the pause is there so that
// a broker that is really away is not asked in a tight loop, and the whole
// write is still bounded by the publisher's timeout.
const (
	retryPause    = 25 * time.Millisecond
	maxRetryPause = 500 * time.Millisecond
)

// awaitConnection waits for the pause, then for the connection to be back.
//
// Publishing while the client is still reconnecting would be buffered rather
// than refused, but the client fails whatever is outstanding each time it
// starts reconnecting, so a record sent into the middle of a reconnect can be
// failed a second time for the same drop.
func (p *Publisher) awaitConnection(ctx context.Context, pause time.Duration) error {
	if err := sleep(ctx, pause); err != nil {
		return err
	}
	conn := p.js.Conn()
	for conn != nil && !conn.IsConnected() {
		if conn.IsClosed() {
			return nats.ErrConnectionClosed
		}
		if err := sleep(ctx, 10*time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
//
// A fetch that fails is tried again, after a pause that doubles from
// fetchRetryStart to fetchRetryCap and starts over once a fetch succeeds. A
// broker electing a new leader answers every request at once with "no
// responders", and a consumer that asked again at once would ask thousands of
// times a second, each failure a line in the log, for as long as the election
// took. Only a connection closed for good, or a request the client refuses to
// make, stops the run.
func (c *Consumer) Run(ctx context.Context) error {
	var (
		held  batchInProgress
		retry = backoff{start: fetchRetryStart, limit: fetchRetryCap}
	)
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
			if errors.Is(err, nats.ErrConnectionClosed) || errors.Is(err, jetstream.ErrInvalidOption) {
				return fmt.Errorf("natssink: fetch: %w", err)
			}
			c.report(fmt.Errorf("natssink: fetch: %w", err))
			c.rollIfDue(ctx, &held)
			c.wait(ctx, retry.next())
			continue
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
			c.rollIfDue(ctx, &held)
			c.wait(ctx, retry.next())
			continue
		}
		retry.reset()
		c.rollIfDue(ctx, &held)
	}
}

// The pause between failed fetches: from a tenth of a second, doubling, to at
// most five. A pause of five seconds is well inside any sensible roll window
// and any ack wait, so nothing gathered is held long enough to be offered
// again, and it is short enough that a broker back from an election is back
// in use within seconds.
const (
	fetchRetryStart = 100 * time.Millisecond
	fetchRetryCap   = 5 * time.Second
)

// wait pauses between failed fetches, and gives up the pause when the run is
// cancelled.
func (c *Consumer) wait(ctx context.Context, d time.Duration) {
	_ = sleep(ctx, d)
}

// backoff is an exponential pause: start, doubled each time up to limit, and
// back to start on reset.
type backoff struct {
	start, limit, current time.Duration
}

func (b *backoff) next() time.Duration {
	if b.current == 0 {
		b.current = b.start
	} else {
		b.current = min(b.current*2, b.limit)
	}
	return b.current
}

func (b *backoff) reset() { b.current = 0 }

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
