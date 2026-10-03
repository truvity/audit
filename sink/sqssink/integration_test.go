package sqssink_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/truvity/audit/sdk/sink"
	"github.com/truvity/audit/sdk/sink/sinktest"
	"github.com/truvity/audit/sink/sqssink"
)

// urlEnv names the SQS endpoint these tests run against: the LocalStack the s3
// tests use, or any other that speaks the API. They skip when it is unset, so
// the hermetic suite stays hermetic.
const urlEnv = "AUDIT_SQS_URL"

// queue creates a queue of this test's own and returns the client and its URL.
func queue(t *testing.T, fifo bool) (*sqs.Client, string) {
	t.Helper()
	endpoint := os.Getenv(urlEnv)
	if endpoint == "" {
		t.Skipf("%s is not set", urlEnv)
	}
	client := sqs.New(sqs.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
	})
	var b [6]byte
	_, _ = rand.Read(b[:])
	name, attrs := "audit-"+hex.EncodeToString(b[:]), map[string]string{}
	if fifo {
		name += ".fifo"
		attrs["FifoQueue"] = "true"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name), Attributes: attrs})
	if err != nil {
		t.Fatalf("create queue: %v", err)
	}
	t.Cleanup(func() {
		_, _ = client.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: out.QueueUrl})
	})
	return client, aws.ToString(out.QueueUrl)
}

// drain consumes a queue into a Memory until it holds want records, or fails.
func drain(t *testing.T, client *sqs.Client, url string, want int) *sink.Memory {
	t.Helper()
	mem := &sink.Memory{}
	c, err := sqssink.NewConsumer(client, mem, sqssink.ConsumerOptions{
		QueueURL: url, Wait: time.Second, Visibility: 30 * time.Second,
		OnError: func(err error) { t.Errorf("consumer: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = c.Run(ctx) }()
	deadline := time.Now().Add(30 * time.Second)
	for mem.Len() < want && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	// Linger, so that a record taken twice shows up as a count above want.
	time.Sleep(time.Second)
	cancel()
	<-done
	return mem
}

func TestLocalStackRoundTrip(t *testing.T) {
	for name, fifo := range map[string]bool{"standard": false, "fifo": true} {
		t.Run(name, func(t *testing.T) {
			client, url := queue(t, fifo)
			p, err := sqssink.NewPublisher(client, sqssink.Options{QueueURL: url})
			if err != nil {
				t.Fatal(err)
			}
			batch := sinktest.Records(25)
			res, err := p.Write(context.Background(), &sink.Request{Records: batch, Delivery: sink.Block})
			if err != nil || res.Accepted != 25 || res.Durability != sink.Queued {
				t.Fatalf("Write = %+v, %v", res, err)
			}
			if fifo {
				// The same batch again: SQS absorbs it by deduplication id.
				if _, err := p.Write(context.Background(), &sink.Request{Records: batch}); err != nil {
					t.Fatal(err)
				}
			}
			got := drain(t, client, url, 25)
			if got.Len() != 25 {
				t.Fatalf("the consumer wrote %d records, want 25", got.Len())
			}
			seen := map[string]bool{}
			for _, r := range got.Records() {
				seen[r.GetId()] = true
			}
			for _, r := range batch {
				if !seen[r.GetId()] {
					t.Errorf("record %s did not arrive", r.GetId())
				}
			}
		})
	}
}

func TestLocalStackPublishToAMissingQueueFails(t *testing.T) {
	client, url := queue(t, false)
	_, _ = client.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: aws.String(url)})
	p, _ := sqssink.NewPublisher(client, sqssink.Options{QueueURL: url, Timeout: 5 * time.Second})
	if res, err := p.Write(context.Background(), &sink.Request{Records: sinktest.Records(1)}); err == nil {
		t.Fatalf("a publish to a queue that is gone reported %+v", res)
	}
}
