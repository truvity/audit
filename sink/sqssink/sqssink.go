// Package sqssink publishes records to an Amazon SQS queue, and consumes them
// again on the other side.
//
// Like the stream in natssink, the queue is a buffer and not a store: the
// writer's put into object storage is where archival begins. What the queue
// gives is an acknowledgement that means SQS has stored the message on several
// servers, which is why a publish reports sink.Queued and never more.
//
// The queue is at-least-once, and on a standard queue a publish that was not
// acknowledged and is sent again can be delivered twice. Every message carries
// its record's id, and the writer on the far side drops a repeat by it. On a
// FIFO queue (a URL ending in .fifo) the id is also the message's
// deduplication id, so SQS itself absorbs a repeat inside its five-minute
// window.
//
// The consumer hands each receive to its target as one batch. It does not roll
// several receives into one object the way natssink's does, which is a
// follow-up: until then, point it at a target that rolls.
package sqssink

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/truvity/audit/internal/telemetry"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

// API is the part of the SQS client this package calls; *sqs.Client satisfies
// it, and so does a test's fake.
type API interface {
	SendMessageBatch(ctx context.Context, in *sqs.SendMessageBatchInput,
		opts ...func(*sqs.Options)) (*sqs.SendMessageBatchOutput, error)
	ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput,
		opts ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessageBatch(ctx context.Context, in *sqs.DeleteMessageBatchInput,
		opts ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error)
	ChangeMessageVisibilityBatch(ctx context.Context, in *sqs.ChangeMessageVisibilityBatchInput,
		opts ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityBatchOutput, error)
}

// RecordIDAttribute is the message attribute that carries the record's id.
const RecordIDAttribute = "record-id"

// maxLinks bounds the links on a consume span.
const maxLinks = 16

// SQS accepts at most ten entries in a batch and, in total, 256 KiB of body.
const (
	maxBatchEntries = 10
	maxBatchBytes   = 256 << 10
)

// Publisher is a Sink that puts records on a queue.
type Publisher struct {
	api      API
	queue    string
	fifo     bool
	timeout  time.Duration
	attempts int
}

// Options configure a publisher.
type Options struct {
	// QueueURL is the queue records are sent to.
	QueueURL string
	// Timeout bounds one Write. Default 10s.
	Timeout time.Duration
	// Attempts is how many times the entries SQS failed for its own reasons
	// are sent. Default 3.
	Attempts int
}

// NewPublisher returns a Sink publishing to an SQS queue.
func NewPublisher(api API, o Options) (*Publisher, error) {
	if api == nil {
		return nil, errors.New("sqssink: an SQS client is required")
	}
	if o.QueueURL == "" {
		return nil, errors.New("sqssink: a queue URL is required")
	}
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	if o.Attempts <= 0 {
		o.Attempts = 3
	}
	return &Publisher{
		api: api, queue: o.QueueURL, fifo: strings.HasSuffix(o.QueueURL, ".fifo"),
		timeout: o.Timeout, attempts: o.Attempts,
	}, nil
}

// Guarantees implements sink.Guarantor.
func (p *Publisher) Guarantees() sink.Durability { return sink.Queued }

// Write implements sink.Sink. It reports Queued, and only once SQS has
// accepted every message it was asked to send.
//
// An entry SQS refuses because of the message itself is a rejection. An entry
// it fails for its own reasons is sent again, with a pause, up to the
// publisher's attempts; if it still fails the write fails, and the caller
// retries the batch, which the record id makes harmless.
func (p *Publisher) Write(ctx context.Context, req *sink.Request) (*sink.Result, error) {
	ctx, done := sink.Observe(ctx, sink.TransportSQS, trace.SpanKindProducer, req)
	res, err := p.write(ctx, req)
	done(res, err)
	return res, err
}

func (p *Publisher) write(ctx context.Context, req *sink.Request) (*sink.Result, error) {
	result := &sink.Result{Durability: sink.Queued}
	if len(req.Records) == 0 {
		return result, nil
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	// The producing span's context travels as message attributes, which is
	// where SQS has room for it, so that the consumer continues the trace.
	carrier := attributeCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)

	var pending []outgoing
	for _, r := range req.Records {
		line, err := record.Canonical(r)
		if err != nil {
			result.Rejected = append(result.Rejected, sink.Rejection{
				ID: r.GetId(), Reason: "cannot be encoded: " + err.Error(),
			})
			continue
		}
		if len(line) > maxBatchBytes {
			result.Rejected = append(result.Rejected, sink.Rejection{
				ID: r.GetId(), Reason: "is larger than one SQS message can be",
			})
			continue
		}
		pending = append(pending, outgoing{id: r.GetId(), tenant: r.GetTenantId(), body: line, trace: carrier})
	}

	pause := 50 * time.Millisecond
	for attempt := 1; len(pending) > 0; attempt++ {
		var failed []outgoing
		for _, chunk := range chunks(pending) {
			again, err := p.send(ctx, chunk, result)
			if err != nil {
				return nil, err
			}
			failed = append(failed, again...)
		}
		if len(failed) == 0 {
			break
		}
		if attempt >= p.attempts {
			return nil, fmt.Errorf("sqssink: SQS did not take %d of %d records after %d attempts",
				len(failed), len(req.Records), attempt)
		}
		if err := sleep(ctx, pause); err != nil {
			return nil, fmt.Errorf("sqssink: waiting to send again: %w", err)
		}
		pause *= 2
		pending = failed
	}
	return result, nil
}

// outgoing is a record on its way to the queue.
type outgoing struct {
	id     string
	tenant string
	body   []byte
	trace  attributeCarrier
}

// attributeCarrier is a message's attributes as a W3C trace-context carrier:
// `traceparent` and `tracestate` as String attributes, which SQS allows
// beside the record-id one (ten are permitted).
type attributeCarrier map[string]types.MessageAttributeValue

func (a attributeCarrier) Get(key string) string { return aws.ToString(a[key].StringValue) }
func (a attributeCarrier) Set(key, value string) {
	a[key] = types.MessageAttributeValue{DataType: aws.String("String"), StringValue: aws.String(value)}
}
func (a attributeCarrier) Keys() []string {
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	return keys
}

// traceAttributes are the names a receive asks for, so that SQS returns them.
var traceAttributes = []string{"traceparent", "tracestate"}

// chunks splits records into batches SQS will take: ten entries, 256 KiB.
func chunks(all []outgoing) [][]outgoing {
	var out [][]outgoing
	var cur []outgoing
	size := 0
	for _, o := range all {
		if len(cur) == maxBatchEntries || size+len(o.body) > maxBatchBytes {
			out = append(out, cur)
			cur, size = nil, 0
		}
		cur = append(cur, o)
		size += len(o.body)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// send sends one batch and returns the records SQS failed and that are worth
// sending again.
func (p *Publisher) send(ctx context.Context, batch []outgoing, result *sink.Result) ([]outgoing, error) {
	entries := make([]types.SendMessageBatchRequestEntry, len(batch))
	for i, o := range batch {
		e := types.SendMessageBatchRequestEntry{
			Id:          aws.String(strconv.Itoa(i)),
			MessageBody: aws.String(string(o.body)),
			MessageAttributes: map[string]types.MessageAttributeValue{
				RecordIDAttribute: {DataType: aws.String("String"), StringValue: aws.String(o.id)},
			},
		}
		if p.fifo {
			group := o.tenant
			if group == "" {
				group = "audit"
			}
			e.MessageGroupId = aws.String(group)
			e.MessageDeduplicationId = aws.String(o.id)
		}
		for k, v := range o.trace {
			e.MessageAttributes[k] = v
		}
		entries[i] = e
	}
	out, err := p.api.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{QueueUrl: aws.String(p.queue), Entries: entries})
	if err != nil {
		return nil, fmt.Errorf("sqssink: send: %w", err)
	}

	// An entry that is neither successful nor failed would be counted accepted
	// below on the strength of the response's silence. Hold SQS to its own
	// count instead.
	if len(out.Successful)+len(out.Failed) != len(batch) {
		return nil, fmt.Errorf("sqssink: SQS answered for %d of %d messages",
			len(out.Successful)+len(out.Failed), len(batch))
	}

	var again []outgoing
	failed := map[string]types.BatchResultErrorEntry{}
	for _, f := range out.Failed {
		failed[aws.ToString(f.Id)] = f
	}
	for i, o := range batch {
		f, bad := failed[strconv.Itoa(i)]
		switch {
		case !bad:
			result.Accepted++
		case f.SenderFault:
			result.Rejected = append(result.Rejected, sink.Rejection{
				ID: o.id, Reason: fmt.Sprintf("SQS refused it: %s: %s", aws.ToString(f.Code), aws.ToString(f.Message)),
			})
		default:
			again = append(again, o)
		}
	}
	return again, nil
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

// Consumer reads records from a queue and hands them to a Sink, which on the
// other side of a deployment is the split writer.
type Consumer struct {
	api        API
	queue      string
	target     sink.Sink
	batch      int32
	wait       int32
	visibility int32
	retryAfter int32
	onError    func(error)
}

// ConsumerOptions configure a consumer.
type ConsumerOptions struct {
	// QueueURL is the queue to read.
	QueueURL string
	// Batch is how many messages are received at once, one to ten.
	// Default 10.
	Batch int
	// Wait is how long a receive waits for messages, which SQS rounds to whole
	// seconds up to twenty. Default 20s.
	Wait time.Duration
	// Visibility is how long a received message is hidden from other consumers
	// while the target writes it. It must outlast the target's write: a message
	// that becomes visible again while it is still being written is delivered
	// twice. Default 60s.
	Visibility time.Duration
	// RetryAfter is how soon a message the target refused is offered again,
	// instead of waiting out the rest of Visibility. Default 5s.
	RetryAfter time.Duration
	// OnError is called for a batch the target refused, a message that could
	// not be decoded, and a call to SQS that failed. A refused message is not
	// deleted, so the queue redelivers it.
	OnError func(error)
}

// NewConsumer binds a consumer to a Sink.
func NewConsumer(api API, target sink.Sink, o ConsumerOptions) (*Consumer, error) {
	if api == nil {
		return nil, errors.New("sqssink: an SQS client is required")
	}
	if target == nil {
		return nil, errors.New("sqssink: a target sink is required")
	}
	if o.QueueURL == "" {
		return nil, errors.New("sqssink: a queue URL is required")
	}
	if o.Batch <= 0 || o.Batch > maxBatchEntries {
		o.Batch = maxBatchEntries
	}
	if o.Wait <= 0 || o.Wait > 20*time.Second {
		o.Wait = 20 * time.Second
	}
	if o.Visibility <= 0 {
		o.Visibility = time.Minute
	}
	if o.RetryAfter <= 0 {
		o.RetryAfter = 5 * time.Second
	}
	return &Consumer{
		api: api, queue: o.QueueURL, target: target, batch: int32(o.Batch),
		wait: int32(o.Wait / time.Second), visibility: int32(o.Visibility / time.Second),
		retryAfter: int32(o.RetryAfter / time.Second), onError: o.OnError,
	}, nil
}

// Run consumes until the context is cancelled.
//
// A message is deleted only after the target has taken it. A target that fails
// leaves it on the queue, and its visibility is shortened to RetryAfter so that
// the retry is prompt rather than a full Visibility away. A receive that fails
// is tried again after a pause that doubles from 100ms to 5s.
func (c *Consumer) Run(ctx context.Context) error {
	pause := time.Duration(0)
	for {
		if ctx.Err() != nil {
			return nil
		}
		out, err := c.api.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:              aws.String(c.queue),
			MaxNumberOfMessages:   c.batch,
			WaitTimeSeconds:       c.wait,
			VisibilityTimeout:     c.visibility,
			MessageAttributeNames: traceAttributes,
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			c.report(fmt.Errorf("sqssink: receive: %w", err))
			pause = min(max(pause*2, 100*time.Millisecond), 5*time.Second)
			_ = sleep(ctx, pause)
			continue
		}
		pause = 0
		c.handle(ctx, out.Messages)
	}
}

// handle writes one receive to the target and settles its messages.
func (c *Consumer) handle(ctx context.Context, msgs []types.Message) {
	var (
		records []*record.Record
		held    []types.Message
		skipped []types.Message
	)
	for _, m := range msgs {
		var r record.Record
		if err := record.Unmarshal([]byte(aws.ToString(m.Body)), &r); err != nil {
			// A message this build cannot decode never will. Leaving it would
			// have the queue offer it forever; it is deleted and reported.
			c.report(fmt.Errorf("sqssink: a message could not be decoded and was skipped: %w", err))
			skipped = append(skipped, m)
			continue
		}
		records = append(records, &r)
		held = append(held, m)
	}
	c.delete(ctx, skipped)
	if len(records) == 0 {
		return
	}
	// The consume span continues the first message's trace and links up to
	// maxLinks others'.
	spanCtx, links := ctx, []trace.Link(nil)
	var parent trace.SpanContext
	for _, m := range held {
		carrier := attributeCarrier(m.MessageAttributes)
		sc := trace.SpanContextFromContext(otel.GetTextMapPropagator().Extract(context.Background(), carrier))
		switch {
		case !sc.IsValid():
		case !parent.IsValid():
			parent = sc
		case len(links) < maxLinks:
			links = append(links, trace.Link{SpanContext: sc})
		}
	}
	if parent.IsValid() {
		spanCtx = trace.ContextWithSpanContext(ctx, parent)
	}
	spanCtx, span := otel.Tracer("github.com/truvity/audit/sink").Start(spanCtx, "audit.sink.consume sqs",
		trace.WithSpanKind(trace.SpanKindConsumer), trace.WithLinks(links...),
		trace.WithAttributes(
			attribute.String(telemetry.AttrTransport, sink.TransportSQS),
			attribute.Int(telemetry.AttrRecords, len(records))))
	res, err := c.target.Write(spanCtx, &sink.Request{Records: records, Delivery: sink.Block})
	if err == nil {
		err = res.Err()
	}
	if err != nil {
		span.SetAttributes(attribute.String(telemetry.AttrOutcome, "error"))
		span.SetStatus(codes.Error, "")
		span.End()
		sink.ConsumeFailed(ctx, sink.TransportSQS)
		c.report(err)
		c.retrySoon(ctx, held)
		return
	}
	span.SetAttributes(attribute.String(telemetry.AttrOutcome, "ok"))
	span.End()
	c.delete(ctx, held)
}

func (c *Consumer) delete(ctx context.Context, msgs []types.Message) {
	// Settling must survive the cancellation that is stopping the run, or a
	// batch the target took would be written again by the next consumer.
	ctx = context.WithoutCancel(ctx)
	for _, chunk := range messageChunks(msgs) {
		entries := make([]types.DeleteMessageBatchRequestEntry, len(chunk))
		for i, m := range chunk {
			entries[i] = types.DeleteMessageBatchRequestEntry{Id: aws.String(strconv.Itoa(i)), ReceiptHandle: m.ReceiptHandle}
		}
		out, err := c.api.DeleteMessageBatch(ctx, &sqs.DeleteMessageBatchInput{QueueUrl: aws.String(c.queue), Entries: entries})
		if err != nil {
			c.report(fmt.Errorf("sqssink: delete: %w", err))
			continue
		}
		for _, f := range out.Failed {
			// The records are written; what failed is the note that says so.
			// The queue will offer them again and the writer drops the repeat.
			c.report(fmt.Errorf("sqssink: delete %s: %s", aws.ToString(f.Id), aws.ToString(f.Message)))
		}
	}
}

func (c *Consumer) retrySoon(ctx context.Context, msgs []types.Message) {
	ctx = context.WithoutCancel(ctx)
	for _, chunk := range messageChunks(msgs) {
		entries := make([]types.ChangeMessageVisibilityBatchRequestEntry, len(chunk))
		for i, m := range chunk {
			entries[i] = types.ChangeMessageVisibilityBatchRequestEntry{
				Id: aws.String(strconv.Itoa(i)), ReceiptHandle: m.ReceiptHandle, VisibilityTimeout: c.retryAfter,
			}
		}
		if _, err := c.api.ChangeMessageVisibilityBatch(ctx, &sqs.ChangeMessageVisibilityBatchInput{
			QueueUrl: aws.String(c.queue), Entries: entries,
		}); err != nil {
			// Not fatal: the message is offered again when Visibility ends.
			c.report(fmt.Errorf("sqssink: change visibility: %w", err))
		}
	}
}

func messageChunks(msgs []types.Message) [][]types.Message {
	var out [][]types.Message
	for len(msgs) > 0 {
		n := min(len(msgs), maxBatchEntries)
		out, msgs = append(out, msgs[:n]), msgs[n:]
	}
	return out
}

func (c *Consumer) report(err error) {
	if c.onError != nil {
		c.onError(err)
	}
}
