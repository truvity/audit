package sqssink_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/truvity/audit/sink/sqssink"
)

// fakeSQS is an in-process queue behind the API the package calls. It keeps
// what SQS keeps that the package relies on: batches of at most ten, messages
// hidden while received, a receipt handle per receive, and on a FIFO queue a
// repeated deduplication id taken once.
type fakeSQS struct {
	mu   sync.Mutex
	now  time.Time
	msgs []*fakeMessage
	next int
	dedu map[string]bool

	sent       []sqs.SendMessageBatchInput
	deleted    int
	visibility []int32

	// fail, when set, decides which entries of a send SQS fails.
	fail func(call int, e types.SendMessageBatchRequestEntry) *types.BatchResultErrorEntry
	// sendErr, when set, fails a whole send.
	sendErr error
}

type fakeMessage struct {
	body      string
	attrs     map[string]types.MessageAttributeValue
	receipt   string
	visibleAt time.Time
}

var _ sqssink.API = (*fakeSQS)(nil)

func newFake() *fakeSQS {
	return &fakeSQS{now: time.Unix(1_700_000_000, 0), dedu: map[string]bool{}}
}

func (f *fakeSQS) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// held is how many messages are on the queue, visible or not.
func (f *fakeSQS) held() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.msgs)
}

func (f *fakeSQS) SendMessageBatch(_ context.Context, in *sqs.SendMessageBatchInput, _ ...func(*sqs.Options)) (*sqs.SendMessageBatchOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, *in)
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	if len(in.Entries) > 10 {
		return nil, fmt.Errorf("TooManyEntriesInBatchRequest: %d", len(in.Entries))
	}
	fifo := strings.HasSuffix(aws.ToString(in.QueueUrl), ".fifo")
	out := &sqs.SendMessageBatchOutput{}
	for _, e := range in.Entries {
		if f.fail != nil {
			if bad := f.fail(len(f.sent), e); bad != nil {
				out.Failed = append(out.Failed, *bad)
				continue
			}
		}
		if fifo {
			id := aws.ToString(e.MessageDeduplicationId)
			if id == "" || e.MessageGroupId == nil {
				return nil, fmt.Errorf("a FIFO queue needs a group and a deduplication id")
			}
			if f.dedu[id] {
				out.Successful = append(out.Successful, types.SendMessageBatchResultEntry{Id: e.Id})
				continue
			}
			f.dedu[id] = true
		}
		f.next++
		f.msgs = append(f.msgs, &fakeMessage{body: aws.ToString(e.MessageBody), attrs: e.MessageAttributes, receipt: strconv.Itoa(f.next)})
		out.Successful = append(out.Successful, types.SendMessageBatchResultEntry{Id: e.Id})
	}
	return out, nil
}

func (f *fakeSQS) ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	f.mu.Lock()
	out := &sqs.ReceiveMessageOutput{}
	for _, m := range f.msgs {
		if len(out.Messages) >= int(in.MaxNumberOfMessages) {
			break
		}
		if m.visibleAt.After(f.now) {
			continue
		}
		f.next++
		m.receipt = strconv.Itoa(f.next)
		m.visibleAt = f.now.Add(time.Duration(in.VisibilityTimeout) * time.Second)
		out.Messages = append(out.Messages, types.Message{
			Body: aws.String(m.body), ReceiptHandle: aws.String(m.receipt), MessageAttributes: m.attrs,
		})
	}
	f.mu.Unlock()
	if len(out.Messages) == 0 {
		// Long polling, shortened: wait a moment so Run does not spin.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Millisecond):
		}
	}
	return out, nil
}

func (f *fakeSQS) DeleteMessageBatch(_ context.Context, in *sqs.DeleteMessageBatchInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range in.Entries {
		for i, m := range f.msgs {
			if m.receipt == aws.ToString(e.ReceiptHandle) {
				f.msgs = append(f.msgs[:i], f.msgs[i+1:]...)
				f.deleted++
				break
			}
		}
	}
	return &sqs.DeleteMessageBatchOutput{}, nil
}

func (f *fakeSQS) ChangeMessageVisibilityBatch(
	_ context.Context, in *sqs.ChangeMessageVisibilityBatchInput, _ ...func(*sqs.Options),
) (*sqs.ChangeMessageVisibilityBatchOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range in.Entries {
		f.visibility = append(f.visibility, e.VisibilityTimeout)
		for _, m := range f.msgs {
			if m.receipt == aws.ToString(e.ReceiptHandle) {
				m.visibleAt = f.now.Add(time.Duration(e.VisibilityTimeout) * time.Second)
			}
		}
	}
	return &sqs.ChangeMessageVisibilityBatchOutput{}, nil
}

// batchOf is a send of raw bodies, for what a producer other than this
// package's publisher might put on the queue.
func batchOf(bodies ...string) *sqs.SendMessageBatchInput {
	in := &sqs.SendMessageBatchInput{QueueUrl: aws.String(standard)}
	for i, b := range bodies {
		in.Entries = append(in.Entries, types.SendMessageBatchRequestEntry{Id: aws.String(strconv.Itoa(i)), MessageBody: aws.String(b)})
	}
	return in
}
