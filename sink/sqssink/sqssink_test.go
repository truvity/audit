package sqssink_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/truvity/audit/sdk/record"
	"github.com/truvity/audit/sdk/sink"
	"github.com/truvity/audit/sdk/sink/sinktest"
	"github.com/truvity/audit/sink/sqssink"
)

const (
	standard = "https://sqs.example.test/queue/audit"
	fifo     = "https://sqs.example.test/queue/audit.fifo"
)

func publisher(t *testing.T, api sqssink.API, url string) *sqssink.Publisher {
	t.Helper()
	p, err := sqssink.NewPublisher(api, sqssink.Options{QueueURL: url, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConformance(t *testing.T) {
	for name, url := range map[string]string{"standard": standard, "fifo": fifo} {
		t.Run(name, func(t *testing.T) {
			sinktest.Run(t, func(t *testing.T) sinktest.Subject {
				f := newFake()
				return sinktest.Subject{
					Sink: publisher(t, f, url), Durability: sink.Queued,
					Count: f.held, Refuses: true,
					// Only a FIFO queue absorbs a repeat; a standard one
					// leaves it to the writer.
					Idempotent: url == fifo,
				}
			})
		})
	}
}

func TestPublisherNeedsAClientAndAQueue(t *testing.T) {
	if _, err := sqssink.NewPublisher(nil, sqssink.Options{QueueURL: standard}); err == nil {
		t.Error("no client was accepted")
	}
	if _, err := sqssink.NewPublisher(newFake(), sqssink.Options{}); err == nil {
		t.Error("no queue was accepted")
	}
}

// SQS takes ten entries in a batch, so a larger write is several calls, and
// the write still reports success only once they all took.
func TestPublisherSplitsIntoBatchesOfTen(t *testing.T) {
	f := newFake()
	res, err := publisher(t, f, standard).Write(context.Background(), &sink.Request{Records: sinktest.Records(23)})
	if err != nil || res.Accepted != 23 {
		t.Fatalf("accepted %v, err %v", res, err)
	}
	if len(f.sent) != 3 {
		t.Fatalf("%d calls, want 3", len(f.sent))
	}
}

func TestPublisherCarriesTheRecordID(t *testing.T) {
	for _, url := range []string{standard, fifo} {
		f := newFake()
		batch := sinktest.Records(1)
		if _, err := publisher(t, f, url).Write(context.Background(), &sink.Request{Records: batch}); err != nil {
			t.Fatal(err)
		}
		e := f.sent[0].Entries[0]
		if got := aws.ToString(e.MessageAttributes[sqssink.RecordIDAttribute].StringValue); got != batch[0].GetId() {
			t.Errorf("%s: record-id attribute = %q, want %q", url, got, batch[0].GetId())
		}
		dedup := aws.ToString(e.MessageDeduplicationId)
		if (url == fifo) != (dedup == batch[0].GetId()) || (url == standard && e.MessageGroupId != nil) {
			t.Errorf("%s: dedup id %q, group %v", url, dedup, e.MessageGroupId)
		}
	}
}

// An entry SQS refuses for what it is is a rejection; one it fails for its own
// reasons is sent again, and the batch is accepted once it takes.
func TestPublisherPartialFailure(t *testing.T) {
	f := newFake()
	batch := sinktest.Records(3)
	f.fail = func(call int, e types.SendMessageBatchRequestEntry) *types.BatchResultErrorEntry {
		id := aws.ToString(e.MessageAttributes[sqssink.RecordIDAttribute].StringValue)
		switch {
		case id == batch[0].GetId():
			return &types.BatchResultErrorEntry{Id: e.Id, SenderFault: true, Code: aws.String("InvalidParameterValue"), Message: aws.String("no")}
		case id == batch[1].GetId() && call == 1:
			return &types.BatchResultErrorEntry{Id: e.Id, Code: aws.String("InternalError")}
		}
		return nil
	}
	res, err := publisher(t, f, standard).Write(context.Background(), &sink.Request{Records: batch})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 2 || len(res.Rejected) != 1 || res.Rejected[0].ID != batch[0].GetId() {
		t.Fatalf("accepted %d, rejected %+v", res.Accepted, res.Rejected)
	}
	if res.Durability != sink.Queued || f.held() != 2 || len(f.sent) != 2 {
		t.Fatalf("durability %v, held %d, calls %d", res.Durability, f.held(), len(f.sent))
	}
}

// A write that SQS did not take whole is not a success, however many entries
// it did take.
func TestPublisherFailsWhenSQSKeepsFailing(t *testing.T) {
	f := newFake()
	f.fail = func(int, types.SendMessageBatchRequestEntry) *types.BatchResultErrorEntry {
		return &types.BatchResultErrorEntry{Id: aws.String("0"), Code: aws.String("ServiceUnavailable")}
	}
	res, err := publisher(t, f, standard).Write(context.Background(), &sink.Request{Records: sinktest.Records(2)})
	if err == nil || res != nil {
		t.Fatalf("got %+v, %v; want an error", res, err)
	}
	if len(f.sent) != 3 {
		t.Fatalf("%d attempts, want 3", len(f.sent))
	}
}

func TestPublisherReturnsASendError(t *testing.T) {
	f := newFake()
	f.sendErr = errors.New("connection reset")
	if _, err := publisher(t, f, standard).Write(context.Background(), &sink.Request{Records: sinktest.Records(1)}); err == nil {
		t.Fatal("a failed send was reported as success")
	}
}

type recorder struct {
	mu   sync.Mutex
	errs []error
}

func (r *recorder) report(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err)
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.errs)
}

// run starts a consumer and returns a function that stops it and waits.
func run(t *testing.T, f *fakeSQS, target sink.Sink, onError func(error)) func() {
	t.Helper()
	c, err := sqssink.NewConsumer(f, target, sqssink.ConsumerOptions{
		QueueURL: standard, Visibility: 30 * time.Second, RetryAfter: 2 * time.Second, OnError: onError,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	return func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for range 500 {
		if ok() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestConsumerWritesThenDeletes(t *testing.T) {
	f, mem := newFake(), &sink.Memory{}
	if _, err := publisher(t, f, standard).Write(context.Background(), &sink.Request{Records: sinktest.Records(25)}); err != nil {
		t.Fatal(err)
	}
	stop := run(t, f, mem, nil)
	defer stop()
	eventually(t, "the queue to drain", func() bool { return mem.Len() == 25 && f.held() == 0 })
}

// A target that refuses leaves the message on the queue, offered again soon
// rather than after the whole visibility timeout; one that then takes it
// ends the loop.
func TestConsumerRetriesWhatTheTargetRefused(t *testing.T) {
	f, mem := newFake(), &sink.Memory{}
	if _, err := publisher(t, f, standard).Write(context.Background(), &sink.Request{Records: sinktest.Records(3)}); err != nil {
		t.Fatal(err)
	}
	var (
		mu    sync.Mutex
		calls int
	)
	target := sink.Func(func(ctx context.Context, req *sink.Request) (*sink.Result, error) {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			return nil, errors.New("the writer is down")
		}
		return mem.Write(ctx, req)
	})
	rec := &recorder{}
	stop := run(t, f, target, rec.report)
	defer stop()

	eventually(t, "the refusal to be reported", func() bool { return rec.count() == 1 })
	eventually(t, "the visibility to be shortened", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.visibility) == 3
	})
	if f.held() != 3 {
		t.Fatalf("%d messages held after a refusal, want 3", f.held())
	}
	if f.visibility[0] != 2 {
		t.Fatalf("visibility set to %d, want the retry delay 2", f.visibility[0])
	}
	f.advance(3 * time.Second)
	eventually(t, "the redelivery", func() bool { return mem.Len() == 3 && f.held() == 0 })
}

func TestConsumerTreatsRejectionsAsFailure(t *testing.T) {
	f := newFake()
	if _, err := publisher(t, f, standard).Write(context.Background(), &sink.Request{Records: sinktest.Records(1)}); err != nil {
		t.Fatal(err)
	}
	target := sink.Func(func(_ context.Context, req *sink.Request) (*sink.Result, error) {
		return &sink.Result{Rejected: []sink.Rejection{{ID: req.Records[0].GetId(), Reason: "no"}}}, nil
	})
	rec := &recorder{}
	stop := run(t, f, target, rec.report)
	defer stop()
	eventually(t, "the rejection to be reported", func() bool { return rec.count() >= 1 })
	if f.held() != 1 {
		t.Fatalf("a rejected message was deleted: %d held", f.held())
	}
}

// A message that cannot be decoded never will, so it is deleted and reported
// instead of being offered for ever.
func TestConsumerDropsWhatItCannotDecode(t *testing.T) {
	f, mem := newFake(), &sink.Memory{}
	if _, err := f.SendMessageBatch(context.Background(), batchOf("not a record", "{")); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	stop := run(t, f, mem, rec.report)
	defer stop()
	eventually(t, "the bad messages to go", func() bool { return f.held() == 0 && rec.count() == 2 })
	if mem.Len() != 0 {
		t.Fatalf("%d records reached the target", mem.Len())
	}
}

func TestConsumerNeedsWhatItUses(t *testing.T) {
	if _, err := sqssink.NewConsumer(nil, sink.Discard, sqssink.ConsumerOptions{QueueURL: standard}); err == nil {
		t.Error("no client was accepted")
	}
	if _, err := sqssink.NewConsumer(newFake(), nil, sqssink.ConsumerOptions{QueueURL: standard}); err == nil {
		t.Error("no target was accepted")
	}
	if _, err := sqssink.NewConsumer(newFake(), sink.Discard, sqssink.ConsumerOptions{}); err == nil {
		t.Error("no queue was accepted")
	}
}

func TestPublisherRefusesAnOversizeRecord(t *testing.T) {
	big := sinktest.Records(1)[0]
	big.Action = strings.Repeat("a", 300<<10)
	f := newFake()
	res, err := publisher(t, f, standard).Write(context.Background(), &sink.Request{Records: []*record.Record{big}})
	if err != nil || res.Accepted != 0 || len(res.Rejected) != 1 || len(f.sent) != 0 {
		t.Fatalf("%+v, %v, %d calls", res, err, len(f.sent))
	}
}
