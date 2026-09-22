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
	consumer jetstream.Consumer
	target   sink.Sink
	batch    int
	onError  func(error)
}

// ConsumerOptions configure a consumer.
type ConsumerOptions struct {
	// Batch is how many records are handed over at once. Default 100.
	Batch int
	// OnError is called for a batch the target refused. The message is not
	// acknowledged, so the stream redelivers it; without this a deployment
	// would watch records go round silently.
	OnError func(error)
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
	return &Consumer{consumer: c, target: target, batch: o.Batch, onError: o.OnError}, nil
}

// Run consumes until the context is cancelled.
//
// A batch is acknowledged only once the target has taken it. A target that
// fails leaves the messages unacknowledged, so the stream redelivers them and
// nothing is lost to a writer that was briefly unable to write.
func (c *Consumer) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return nil //nolint:nilerr // a cancelled run is not a failure
		}
		batch, err := c.consumer.Fetch(c.batch, jetstream.FetchMaxWait(time.Second))
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, nats.ErrTimeout) {
				continue
			}
			return fmt.Errorf("natssink: fetch: %w", err)
		}

		var (
			messages []jetstream.Msg
			records  []*record.Record
		)
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
			messages = append(messages, msg)
			records = append(records, &r)
		}
		if err := batch.Error(); err != nil {
			c.report(fmt.Errorf("natssink: batch: %w", err))
		}
		if len(records) == 0 {
			continue
		}

		res, err := c.target.Write(ctx, &sink.Request{Records: records, Delivery: sink.Block})
		if err == nil {
			err = res.Err()
		}
		if err != nil {
			c.report(err)
			// Not acknowledged, so the stream brings them back.
			continue
		}
		for _, msg := range messages {
			if err := msg.Ack(); err != nil {
				c.report(fmt.Errorf("natssink: acknowledge: %w", err))
			}
		}
	}
}

func (c *Consumer) report(err error) {
	if c.onError != nil {
		c.onError(err)
	}
}
