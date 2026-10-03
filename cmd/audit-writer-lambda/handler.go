package main

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/aws/aws-lambda-go/events"

	"github.com/truvity/audit/sdk/record"
	"github.com/truvity/audit/sdk/sink"
)

// queueMetrics is what the handler reports about the queue: the age of each message at
// receive. internal/telemetry.Queue satisfies it.
type queueMetrics interface {
	Received(transport string, age time.Duration)
}

// handler is one invocation's work: an SQS batch in, the messages that did not
// make it out.
type handler struct {
	// target is the writer behind its `require` guard.
	target sink.Sink
	// age records how long each message waited. Nil records nothing.
	age queueMetrics
	// before runs once per invocation, ahead of the write: it re-reads the legal
	// holds, which a frozen environment cannot do in the background.
	before func(context.Context)
	// after runs once per invocation, behind the write: it flushes telemetry,
	// because an environment that is frozen exports nothing.
	after func(context.Context)
	now   func() time.Time
	log   *slog.Logger
}

// Handle writes a batch and answers with partial batch responses
// (ReportBatchItemFailures): only the messages that were not archived are
// returned, so only they go back to the queue. It returns an error, and so
// fails the whole batch, only for what no message is to blame for.
//
// A batch is written as one call to the writer, which is all-or-nothing about
// the copies it was handed: it returns once every record is in the archive, was
// seen before, or was dead-lettered. So the outcomes are:
//
//   - the write succeeds: every message that decoded is archived and is not
//     reported, which is what makes SQS delete it;
//   - the write fails: every message that decoded is reported and comes back
//     after the visibility timeout, and the writer's deduplication absorbs any
//     of them that did land;
//   - the sink refuses a record by id: that message is reported, and after
//     maxReceiveCount deliveries lands in the DLQ, where an alarm is on it;
//   - a message that is not a record: reported at once, for the same reason. A
//     poison message is the DLQ's job, and silently deleting it would be a
//     record lost without anyone being told.
func (h *handler) Handle(ctx context.Context, ev events.SQSEvent) (events.SQSEventResponse, error) {
	if h.before != nil {
		h.before(ctx)
	}
	if h.after != nil {
		defer h.after(ctx)
	}
	var (
		fails   []events.SQSBatchItemFailure
		records []*record.Record
		decoded []decodedMessage
	)
	for _, m := range ev.Records {
		h.received(m)
		var r record.Record
		if err := record.Unmarshal([]byte(m.Body), &r); err != nil {
			h.log.Error("a message could not be decoded; it is returned to the queue and will reach the DLQ",
				"message", m.MessageId, "error", err)
			fails = append(fails, events.SQSBatchItemFailure{ItemIdentifier: m.MessageId})
			continue
		}
		records = append(records, &r)
		decoded = append(decoded, decodedMessage{message: m.MessageId, record: r.GetId()})
	}
	if len(records) == 0 {
		return events.SQSEventResponse{BatchItemFailures: fails}, nil
	}

	res, err := h.target.Write(ctx, &sink.Request{Records: records, Delivery: sink.Block})
	if err != nil {
		h.log.Error("the writer did not take the batch; every message in it goes back to the queue",
			"messages", len(ev.Records), "error", err)
		return events.SQSEventResponse{BatchItemFailures: allBut(ev.Records, fails)}, nil
	}
	if rej := res.Err(); rej != nil {
		h.log.Error("the writer refused records of the batch", "error", rej)
		refused := map[string]bool{}
		for _, x := range res.Rejected {
			refused[x.ID] = true
		}
		// Two messages may carry one record; a refusal by id settles both.
		for _, d := range decoded {
			if refused[d.record] {
				fails = append(fails, events.SQSBatchItemFailure{ItemIdentifier: d.message})
			}
		}
	}
	return events.SQSEventResponse{BatchItemFailures: fails}, nil
}

// decodedMessage pairs a message with the record it carried.
type decodedMessage struct{ message, record string }

// allBut is every message of the batch, in order, that is not already a failure.
func allBut(msgs []events.SQSMessage, already []events.SQSBatchItemFailure) []events.SQSBatchItemFailure {
	out := append([]events.SQSBatchItemFailure(nil), already...)
	have := make(map[string]bool, len(already))
	for _, f := range already {
		have[f.ItemIdentifier] = true
	}
	for _, m := range msgs {
		if !have[m.MessageId] {
			out = append(out, events.SQSBatchItemFailure{ItemIdentifier: m.MessageId})
		}
	}
	return out
}

// received records how long a message waited, from the SentTimestamp SQS stamps
// on it, in milliseconds since the epoch. A message without one is not counted:
// a fabricated age would be worse than none.
func (h *handler) received(m events.SQSMessage) {
	if h.age == nil {
		return
	}
	ms, err := strconv.ParseInt(m.Attributes["SentTimestamp"], 10, 64)
	if err != nil {
		return
	}
	now := h.now
	if now == nil {
		now = time.Now
	}
	h.age.Received(sink.TransportSQS, now().Sub(time.UnixMilli(ms)))
}
