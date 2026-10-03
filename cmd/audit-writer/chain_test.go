package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/truvity/audit/sinkserver"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/truvity/audit/internal/config"
	"github.com/truvity/audit/internal/registry"
	"github.com/truvity/audit/sdk/record"
	"github.com/truvity/audit/sdk/sink"
	"github.com/truvity/audit/sink/sqssink"
)

// queue is a queue that keeps what was sent to it.
type queue struct {
	mu   sync.Mutex
	sent []string
	urls []string
}

func (q *queue) SendMessageBatch(_ context.Context, in *sqs.SendMessageBatchInput, _ ...func(*sqs.Options)) (*sqs.SendMessageBatchOutput, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := &sqs.SendMessageBatchOutput{}
	for _, e := range in.Entries {
		q.sent = append(q.sent, *e.MessageBody)
		q.urls = append(q.urls, *in.QueueUrl)
		out.Successful = append(out.Successful, types.SendMessageBatchResultEntry{Id: e.Id})
	}
	return out, nil
}

func (*queue) ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	return &sqs.ReceiveMessageOutput{}, nil
}

func (*queue) DeleteMessageBatch(context.Context, *sqs.DeleteMessageBatchInput, ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error) {
	return &sqs.DeleteMessageBatchOutput{}, nil
}

func (*queue) ChangeMessageVisibilityBatch(
	context.Context, *sqs.ChangeMessageVisibilityBatchInput, ...func(*sqs.Options),
) (*sqs.ChangeMessageVisibilityBatchOutput, error) {
	return &sqs.ChangeMessageVisibilityBatchOutput{}, nil
}

var _ sqssink.API = (*queue)(nil)

func load(t *testing.T, body string) *config.Writer {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWriter(p)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

const receiverHead = "mode: receiver\ndeployment: /d.yaml\nanonymousWrites: true\n"

// chain builds a receiver's chain the way run does.
func chain(t *testing.T, cfg *config.Writer) (sink.Sink, error) {
	t.Helper()
	to, stop, err := forwardTo(context.Background(), cfg)
	if err != nil {
		return nil, err
	}
	t.Cleanup(stop)
	return guard(&sinkserver.Receiver{To: to}, cfg.Require)
}

func oneRecord() *sink.Request {
	entry := registry.Entry{Source: "app", Version: "1"}
	return &sink.Request{Delivery: sink.Block, Records: []*record.Record{registrationRecord(entry, "test")}}
}

func TestAReceiverForwardsToSQSAndReportsQueued(t *testing.T) {
	q := &queue{}
	newSQS = func(_ context.Context, region string) (sqssink.API, error) {
		if region != "eu-west-1" {
			t.Errorf("the region of the file did not reach the client: %q", region)
		}
		return q, nil
	}
	cfg := load(t, receiverHead+"forward: {sqs: {queueUrl: 'https://sqs.eu-west-1.amazonaws.com/ACCOUNT/audit.fifo', region: eu-west-1, fifo: true}}\n")
	if cfg.Require != "queued" {
		t.Fatalf("a receiver's default require is queued, got %q", cfg.Require)
	}
	front, err := chain(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := sink.Guarantees(front); got != sink.Queued {
		t.Fatalf("the chain guarantees %v, want queued", got)
	}
	res, err := front.Write(context.Background(), oneRecord())
	if err != nil {
		t.Fatal(err)
	}
	if res.Durability != sink.Queued || len(q.sent) != 1 || !strings.HasSuffix(q.urls[0], "audit.fifo") {
		t.Errorf("the record did not go to the queue as queued: %v %v", res.Durability, q.urls)
	}
}

func TestTheLogSinkIsOnlyForRequireLogged(t *testing.T) {
	cfg := load(t, receiverHead+"require: logged\nforward: {log: {}}\n")
	front, err := chain(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := sink.Guarantees(front); got != sink.Logged {
		t.Fatalf("the log chain guarantees %v, want logged", got)
	}
	// The guard refuses anyway, whatever the file said: a log chain held to
	// queued never starts.
	to, stop, err := forwardTo(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if _, err := guard(&sinkserver.Receiver{To: to}, "queued"); err == nil || !strings.Contains(err.Error(), "refusing to start") {
		t.Errorf("a log chain was accepted at require: queued: %v", err)
	}
}

func TestTheGuardRefusesAReceiverThatCannotGiveWhatIsRequired(t *testing.T) {
	newSQS = func(context.Context, string) (sqssink.API, error) { return &queue{}, nil }
	cfg := load(t, receiverHead+"forward: {sqs: {queueUrl: 'https://sqs.example.test/ACCOUNT/audit'}}\n")
	to, stop, err := forwardTo(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	_, err = guard(&sinkserver.Receiver{To: to}, "archived")
	if err == nil || !strings.Contains(err.Error(), "queued at best") {
		t.Fatalf("a queue-only chain was accepted at require: archived: %v", err)
	}
}

func TestAWriterIsArchivedAndTheGuardHoldsIt(t *testing.T) {
	cfg := load(t, "deployment: /d.yaml\nanonymousWrites: true\narchive: {bucket: {name: b}}\n")
	if cfg.Require != "archived" {
		t.Errorf("a writer's default require is archived, got %q", cfg.Require)
	}
	// A chain that says nothing about its durability never meets a requirement.
	silent := sink.Func(func(context.Context, *sink.Request) (*sink.Result, error) { return &sink.Result{}, nil })
	if _, err := guard(silent, cfg.Require); err == nil {
		t.Error("a sink that guarantees nothing was accepted")
	}
}

func TestAWriterConsumesFromSQSOrNATSAsConfigured(t *testing.T) {
	cfg := load(t, "deployment: /d.yaml\nanonymousWrites: true\narchive: {bucket: {name: b}}\n"+
		"consume: {sqs: {queueUrl: 'https://sqs.example.test/ACCOUNT/audit', batch: 5, visibility: 2m}}\n")
	if cfg.Consume == nil || cfg.Consume.SQS == nil || cfg.Consume.SQS.Batch != 5 || cfg.Consume.SQS.Visibility.D().Minutes() != 2 {
		t.Fatalf("consume.sqs did not load: %+v", cfg.Consume)
	}
	newSQS = func(context.Context, string) (sqssink.API, error) { return &queue{}, nil }
	stop, err := consumeSQS(context.Background(), cfg.Consume.SQS, sink.Discard, nil)
	if err != nil {
		t.Fatal(err)
	}
	stop()

	// The stream shorthand is consume.nats, carried with its defaults.
	cfg = load(t, "deployment: /d.yaml\nanonymousWrites: true\narchive: {bucket: {name: b}}\nstream: {nats: {url: 'nats://n:4222'}}\n")
	if cfg.Consume == nil || cfg.Consume.NATS == nil || cfg.Consume.NATS.Name != "AUDIT" || cfg.Consume.NATS.Batch != 100 {
		t.Errorf("stream was not read as consume.nats with its defaults: %+v", cfg.Consume)
	}
}

func TestTheStreamShorthandIsTheReceiversForwardNATS(t *testing.T) {
	cfg := load(t, receiverHead+"stream: {nats: {url: 'nats://n:4222'}, name: AUDIT}\n")
	if cfg.Forward == nil || cfg.Forward.NATS == nil || cfg.Forward.NATS.NATS.URL != "nats://n:4222" || cfg.Require != "queued" {
		t.Errorf("stream was not read as forward.nats: %+v require=%s", cfg.Forward, cfg.Require)
	}
}
