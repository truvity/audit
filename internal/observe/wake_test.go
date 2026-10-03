package observe_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/truvity/audit/internal/observe"
)

// fakeQueue hands out a message per receive, failing the first.
type fakeQueue struct {
	received, deleted atomic.Int32
}

func (q *fakeQueue) ReceiveMessage(ctx context.Context, _ *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	switch q.received.Add(1) {
	case 1:
		return nil, errors.New("the queue is away")
	case 2:
		return &sqs.ReceiveMessageOutput{Messages: []sqstypes.Message{{ReceiptHandle: aws.String("h")}}}, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (q *fakeQueue) DeleteMessageBatch(context.Context, *sqs.DeleteMessageBatchInput, ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error) {
	q.deleted.Add(1)
	return &sqs.DeleteMessageBatchOutput{}, nil
}

// A queue that fails costs a wake-up and is waited out; a message wakes the
// indexer and is deleted, whatever it said.
func TestAQueueMessageWakesAnIndexerAndIsDeleted(t *testing.T) {
	q := &fakeQueue{}
	wake := make(chan struct{}, 1)
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); observe.SQSWake(ctx, q, "https://sqs.example/q", wake, nil) }()
	defer func() { stop(); <-done }()

	select {
	case <-wake:
	case <-time.After(10 * time.Second):
		t.Fatal("a message did not wake the indexer")
	}
	deadline := time.Now().Add(5 * time.Second)
	for q.deleted.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the message was not deleted")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Signals while a pass runs are one: the channel never blocks the sender.
func TestSignalsCoalesceAndNeverBlock(t *testing.T) {
	wake := make(chan struct{}, 1)
	for i := 0; i < 100; i++ {
		observe.Signal(wake)
	}
	if len(wake) != 1 {
		t.Fatalf("%d signals pending, want 1", len(wake))
	}
}
