package telemetry

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Queue is what a process that takes records off a queue reports about the
// queue: how long a message waited.
//
// The age of a message at receive is the number to watch, and a depth is not:
// a deep queue that is draining is healthy, and a shallow one whose oldest
// message is an hour old is not. The depth and the DLQ are the platform's own
// metrics (CloudWatch's ApproximateAgeOfOldestMessage and
// ApproximateNumberOfMessagesVisible), and the alarms on them are in
// deploy/pulumi; this is the per-message view that says whether the writer is
// keeping up and by how much.
type Queue struct {
	age metric.Float64Histogram
}

// NewQueue makes the queue instruments on the given provider, normally the
// global one Start installed.
func NewQueue(provider metric.MeterProvider) (*Queue, error) {
	m := provider.Meter("github.com/truvity/audit/queue")
	age, err := m.Float64Histogram("audit.queue.message.age", metric.WithUnit("s"), // audit:not-an-action — a metric name
		metric.WithDescription("Seconds from a message being sent to the queue to a consumer receiving it, "+
			"per transport. Includes the waits of redeliveries, so a message that keeps failing shows as a long tail."),
		metric.WithExplicitBucketBoundaries(0.1, 0.5, 1, 5, 15, 30, 60, 120, 300, 900, 3600))
	if err != nil {
		return nil, fmt.Errorf("telemetry: audit.queue.message.age: %w", err)
	}
	return &Queue{age: age}, nil
}

// Received records the age of one message at receive. A negative age, which
// clock skew between the sender and the receiver can produce, is recorded as
// zero rather than dropped, so the count stays the count of messages.
func (q *Queue) Received(transport string, age time.Duration) {
	q.age.Record(context.Background(), max(age, 0).Seconds(),
		metric.WithAttributes(attribute.String(AttrTransport, transport)))
}
