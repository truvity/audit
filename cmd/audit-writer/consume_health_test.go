package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nats-io/nats.go"
)

func healthStatus(h http.Handler) int {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	return rec.Code
}

// A consumer that ends without being asked to leaves a writer that answers its
// probes and reads nothing; the health check has to say so.
func TestAConsumerThatStopsMakesTheHealthCheckFail(t *testing.T) {
	url, _ := stream(t)
	health := &healthState{}
	if got := healthStatus(health); got != http.StatusOK {
		t.Fatalf("healthz before anything stopped = %d, want 200", got)
	}

	var conn *nats.Conn
	o := opts(url, 10)
	o.OnStopped = health.consumerStopped
	o.connHook = func(c *nats.Conn) { conn = c }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, err := consume(ctx, o, &target{})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	conn.Close() // what a really closed connection does to the running fetch
	eventually(t, func() bool { return healthStatus(health) == http.StatusServiceUnavailable },
		"healthz stayed healthy after the consumer stopped")
}

// An orderly shutdown stops the consumer too, and is no fault.
func TestAnOrderlyShutdownKeepsTheHealthCheckPassing(t *testing.T) {
	url, _ := stream(t)
	health := &healthState{}
	o := opts(url, 10)
	o.OnStopped = health.consumerStopped

	ctx, cancel := context.WithCancel(context.Background())
	stop, err := consume(ctx, o, &target{})
	if err != nil {
		t.Fatal(err)
	}
	cancel() // SIGTERM arrives as a cancelled context
	stop()

	if got := healthStatus(health); got != http.StatusOK {
		t.Fatalf("healthz after an orderly shutdown = %d, want 200", got)
	}

	// Stopping through stop alone, with the context still live, is orderly too.
	stop2, err := consume(context.Background(), o, &target{})
	if err != nil {
		t.Fatal(err)
	}
	stop2()
	if got := healthStatus(health); got != http.StatusOK {
		t.Fatalf("healthz after stop() = %d, want 200", got)
	}
}

func TestConsumerStoppedWithoutAReasonStillFails(t *testing.T) {
	health := &healthState{}
	health.consumerStopped(nil)
	if got := healthStatus(health); got != http.StatusServiceUnavailable {
		t.Fatalf("healthz = %d, want 503", got)
	}
}
