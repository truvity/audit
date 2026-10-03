package observe

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/nats-io/nats.go"
)

// A wake-up shortens the wait for the next pass and carries nothing a pass
// depends on: a bucket notification that was lost, repeated or reordered costs
// latency, and the poll is what makes it cost no more. So a wake is a channel
// of capacity one that is signalled without blocking, and many signals while a
// pass runs are one.

// Signal wakes whoever reads the channel, without waiting for them.
func Signal(c chan<- struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

// NATSWake signals on every message of a subject. The messages are the
// bucket's notifications, forwarded by whatever the deployment uses for that,
// and their content is not read.
//
// The connection reconnects for as long as the process lives, and a refused or
// lost one is only a quieter wake: the poll goes on.
func NATSWake(url, tokenFile, subject string, wake chan<- struct{}, log *slog.Logger) (stop func(), err error) {
	if log == nil {
		log = slog.Default()
	}
	opts := []nats.Option{
		nats.Name("audit-observe"),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				log.Warn("the wake-up subject's connection was lost; polling carries on", "error", err)
			}
		}),
	}
	if tokenFile != "" {
		// Read on every connect, because the token rotates.
		opts = append(opts, nats.TokenHandler(func() string {
			b, err := os.ReadFile(tokenFile)
			if err != nil {
				log.Error("reading the NATS token", "file", tokenFile, "error", err)
				return ""
			}
			return strings.TrimSpace(string(b))
		}), nats.IgnoreAuthErrorAbort())
	}
	conn, err := nats.Connect(url, opts...)
	if err != nil {
		return nil, fmt.Errorf("observe: connecting to %s for wake-ups: %w", url, err)
	}
	sub, err := conn.Subscribe(subject, func(*nats.Msg) { Signal(wake) })
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("observe: subscribing to %s: %w", subject, err)
	}
	return func() { _ = sub.Unsubscribe(); _ = conn.Drain() }, nil
}

// SQSAPI is the part of the SQS client the wake-up uses.
type SQSAPI interface {
	ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, opts ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessageBatch(ctx context.Context, in *sqs.DeleteMessageBatchInput, opts ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error)
}

// SQSWake long-polls a queue of bucket notifications and signals on each
// message it takes, which it then deletes: observe has a queue of its own, and
// the notification is only a reason to look. It returns when the context ends.
// A queue that fails is waited out and tried again, never fatal.
func SQSWake(ctx context.Context, api SQSAPI, queueURL string, wake chan<- struct{}, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	pause := time.Second
	for ctx.Err() == nil {
		out, err := api.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 20,
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warn("reading the wake-up queue failed; polling carries on", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(pause):
			}
			pause = min(pause*2, time.Minute)
			continue
		}
		pause = time.Second
		if len(out.Messages) == 0 {
			continue
		}
		Signal(wake)
		entries := make([]sqstypes.DeleteMessageBatchRequestEntry, 0, len(out.Messages))
		for i, m := range out.Messages {
			entries = append(entries, sqstypes.DeleteMessageBatchRequestEntry{
				Id: aws.String(strconv.Itoa(i)), ReceiptHandle: m.ReceiptHandle,
			})
		}
		if _, err := api.DeleteMessageBatch(ctx, &sqs.DeleteMessageBatchInput{
			QueueUrl: aws.String(queueURL), Entries: entries,
		}); err != nil && ctx.Err() == nil {
			// The message comes back and is one more wake-up.
			log.Warn("deleting wake-up messages failed", "error", err)
		}
	}
}
