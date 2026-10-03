package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/truvity/audit/internal/config"
	"github.com/truvity/audit/sdk/sink"
	"github.com/truvity/audit/sdk/sink/logsink"
	"github.com/truvity/audit/sink/sqssink"
)

// newSQS is how a queue is reached. The credentials are the SDK's ambient ones
// and nothing else: on Kubernetes the pod's workload identity (EKS Pod Identity
// or IRSA), so the file holds no secret and the process asks for none. A test
// replaces it with a fake queue.
var newSQS = func(ctx context.Context, region string) (sqssink.API, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("loading the AWS configuration for SQS: %w", err)
	}
	return sqs.NewFromConfig(cfg), nil
}

// forwardTo builds a receiver's onward hop from the configuration: the one
// transport `forward` names. The returned function releases what it opened.
func forwardTo(ctx context.Context, cfg *config.Writer) (sink.Sink, func(), error) {
	f := cfg.Forward
	switch {
	case f == nil:
		return nil, nil, errors.New("a receiver needs forward: there is nowhere to send what it takes")
	case f.NATS != nil:
		return publisherFor(ctx, streamOptions{
			URL: f.NATS.NATS.URL, TokenFile: f.NATS.NATS.TokenFile, Stream: f.NATS.Name, Durable: f.NATS.Consumer,
			Batch: f.NATS.Batch, AckWait: f.NATS.AckWait.D(),
		})
	case f.SQS != nil:
		api, err := newSQS(ctx, f.SQS.Region)
		if err != nil {
			return nil, nil, err
		}
		p, err := sqssink.NewPublisher(api, sqssink.Options{QueueURL: f.SQS.QueueURL})
		if err != nil {
			return nil, nil, err
		}
		slog.Info("publishing to the queue", "queue", f.SQS.QueueURL, "fifo", f.SQS.FIFO)
		return p, func() {}, nil
	case f.Log != nil:
		slog.Warn("forwarding to the log: a record is kept for as long as the log pipeline keeps it, and no longer")
		return logsink.New(logsink.Options{}), func() {}, nil
	}
	return nil, nil, errors.New("forward names no transport")
}

// guard holds a chain to `require`: it refuses at start-up when the chain can
// never give it, and fails any write acknowledged below it.
func guard(front sink.Sink, require string) (sink.Sink, error) {
	least, err := sink.ParseDurability(require)
	if err != nil {
		return nil, fmt.Errorf("require: %w", err)
	}
	guarded, err := sink.Guard(front, least)
	if err != nil {
		return nil, fmt.Errorf("refusing to start with require: %s: %w", require, err)
	}
	return guarded, nil
}

// consumeSQS runs a consumer on a queue into the writer until the context is
// cancelled; the returned function waits for it to stop. A message is deleted
// only once the writer has taken it.
func consumeSQS(ctx context.Context, q *config.ConsumeSQS, target sink.Sink, onStopped func(error)) (func(), error) {
	api, err := newSQS(ctx, q.Region)
	if err != nil {
		return nil, err
	}
	c, err := sqssink.NewConsumer(api, target, sqssink.ConsumerOptions{
		QueueURL:   q.QueueURL,
		Batch:      q.Batch,
		Visibility: q.Visibility.D(),
		OnError: func(err error) {
			// The message is not deleted, so the queue brings it back.
			slog.Error("the writer refused a batch from the queue", "error", err)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("writer: %w", err)
	}
	running, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := c.Run(running); err != nil && ctx.Err() == nil && running.Err() == nil {
			slog.Error("the queue consumer stopped", "error", err)
			if onStopped != nil {
				onStopped(err)
			}
		}
	}()
	slog.Info("consuming the queue", "queue", q.QueueURL)
	return func() { cancel(); <-done }, nil
}
