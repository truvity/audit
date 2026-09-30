package natssink_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
	"github.com/truvity/audit/sink/natssink"
)

// dropsAcks is a JetStream whose first acknowledgements are lost to a
// reconnect: the message reaches the stream, and the client reports
// nats.ErrDisconnected for it anyway, which is what the client does with every
// acknowledgement outstanding when its connection drops.
type dropsAcks struct {
	jetstream.JetStream
	drop      atomic.Int32
	published atomic.Int32
}

func (d *dropsAcks) PublishMsgAsync(m *nats.Msg, opts ...jetstream.PublishOpt) (jetstream.PubAckFuture, error) {
	d.published.Add(1)
	f, err := d.JetStream.PublishMsgAsync(m, opts...)
	if err != nil || d.drop.Add(-1) < 0 {
		return f, err
	}
	// Wait for the stream to have the record, then lose the ack: the worst
	// case, where a retry is a duplicate the stream must absorb.
	select {
	case <-f.Ok():
	case err := <-f.Err():
		return nil, err
	}
	return failed{msg: m, err: nats.ErrDisconnected}, nil
}

type failed struct {
	msg *nats.Msg
	err error
}

func (f failed) Ok() <-chan *jetstream.PubAck { return nil }
func (f failed) Err() <-chan error {
	ch := make(chan error, 1)
	ch <- f.err
	return ch
}
func (f failed) Msg() *nats.Msg { return f.msg }

// A block record in flight when the connection drops is published again once
// it is back, and the caller sees success rather than the drop. The stream
// holds it once: the message id makes the second publish a duplicate.
func TestARecordInFlightAcrossAReconnectIsPublishedAgain(t *testing.T) {
	js, s, _ := stream(t)
	lossy := &dropsAcks{JetStream: js}
	lossy.drop.Store(2)
	p, err := natssink.NewPublisher(lossy, natssink.Options{Subject: subject})
	if err != nil {
		t.Fatal(err)
	}

	records := []*record.Record{one(t), one(t), one(t)}
	res, err := p.Write(context.Background(), &sink.Request{Records: records, Delivery: sink.Block})
	if err != nil {
		t.Fatalf("a reconnect failed the write: %v", err)
	}
	if res.Accepted != len(records) {
		t.Fatalf("accepted %d, want %d", res.Accepted, len(records))
	}
	if got := lossy.published.Load(); got != int32(len(records)+2) {
		t.Fatalf("%d publishes, want %d: the two whose acks were lost, sent again", got, len(records)+2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := s.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.State.Msgs != uint64(len(records)) {
		t.Fatalf("the stream holds %d messages for %d records: a retry was written twice",
			info.State.Msgs, len(records))
	}
}

// refuses is a JetStream whose stream refuses every record, for a reason that
// is not the connection.
type refuses struct {
	jetstream.JetStream
	published atomic.Int32
}

func (r *refuses) PublishMsgAsync(m *nats.Msg, _ ...jetstream.PublishOpt) (jetstream.PubAckFuture, error) {
	r.published.Add(1)
	return failed{msg: m, err: errors.New("nats: maximum messages exceeded")}, nil
}

// A refusal is not a reconnect: it would be the same refusal a moment later,
// so it is the caller's at once.
func TestARefusalIsNotPublishedAgain(t *testing.T) {
	js, _, _ := stream(t)
	r := &refuses{JetStream: js}
	p, _ := natssink.NewPublisher(r, natssink.Options{Subject: subject})
	if _, err := p.Write(context.Background(), &sink.Request{Records: []*record.Record{one(t)}}); err == nil {
		t.Fatal("a refused record was reported as taken")
	}
	if got := r.published.Load(); got != 1 {
		t.Fatalf("%d publishes of a refused record, want 1", got)
	}
}

// A connection that stays away is waited for only as long as the publisher's
// timeout, and the caller is told why.
func TestAConnectionThatStaysAwayFailsWithinTheTimeout(t *testing.T) {
	js, _, _ := stream(t)
	lossy := &dropsAcks{JetStream: js}
	lossy.drop.Store(1 << 30)
	p, _ := natssink.NewPublisher(lossy, natssink.Options{Subject: subject, Timeout: 300 * time.Millisecond})

	start := time.Now()
	_, err := p.Write(context.Background(), &sink.Request{Records: []*record.Record{one(t)}})
	if !errors.Is(err, nats.ErrDisconnected) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the disconnect or the deadline", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("the write took %s against a timeout of 300ms", took)
	}
}

// The real thing: the client's own connection is dropped and reopened while
// records are in flight, many times over, and every write still succeeds with
// each record on the stream exactly once.
func TestWritesSurviveReconnectsOfTheRealConnection(t *testing.T) {
	js, s, conn := stream(t)
	p, _ := natssink.NewPublisher(js, natssink.Options{Subject: subject})

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				_ = conn.ForceReconnect()
			}
		}
	}()

	var sent int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		batch := make([]*record.Record, 0, 50)
		for range 50 {
			batch = append(batch, one(t))
		}
		res, err := p.Write(context.Background(), &sink.Request{Records: batch, Delivery: sink.Block})
		if err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("a write failed across a reconnect after %d records: %v", sent, err)
		}
		sent += res.Accepted
	}
	close(stop)
	wg.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := s.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.State.Msgs != uint64(sent) {
		t.Fatalf("the stream holds %d messages for %d records", info.State.Msgs, sent)
	}
}

// failingFetches is a consumer on a broker that answers every pull at once
// with an error, as one does while its stream elects a leader.
type failingFetches struct {
	jetstream.Consumer
	fetches atomic.Int32
	fail    atomic.Bool
}

type emptyBatch struct{ err error }

func (b emptyBatch) Messages() <-chan jetstream.Msg {
	ch := make(chan jetstream.Msg)
	close(ch)
	return ch
}
func (b emptyBatch) Error() error { return b.err }

func (f *failingFetches) Fetch(int, ...jetstream.FetchOpt) (jetstream.MessageBatch, error) {
	f.fetches.Add(1)
	if f.fail.Load() {
		return emptyBatch{err: nats.ErrNoResponders}, nil
	}
	return emptyBatch{}, nil
}

// A broker that fails every pull at once is asked again after a pause that
// grows, not in a tight loop: over a second, a handful of attempts rather than
// the thousands that filled a log in seconds.
func TestFailingFetchesAreRetriedWithBackoff(t *testing.T) {
	c := &failingFetches{}
	c.fail.Store(true)
	var reported atomic.Int32
	consumer, err := natssink.NewConsumer(c, &sink.Memory{}, natssink.ConsumerOptions{
		OnError: func(error) { reported.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := consumer.Run(ctx); err != nil {
		t.Fatalf("the consumer stopped with %v; a failing broker is waited out", err)
	}
	// 0, 0.1, 0.3, 0.7 s: four attempts in the first second, at most.
	if n := c.fetches.Load(); n < 2 || n > 5 {
		t.Fatalf("%d fetches in a second of failures, want a handful", n)
	}
	if reported.Load() == 0 {
		t.Fatal("the failures were not reported")
	}
}

// A connection closed for good is not waited out: the run ends and says so.
func TestAClosedConnectionEndsTheRun(t *testing.T) {
	consumer, _ := natssink.NewConsumer(closedFetches{}, &sink.Memory{}, natssink.ConsumerOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := consumer.Run(ctx); !errors.Is(err, nats.ErrConnectionClosed) {
		t.Fatalf("err = %v, want the closed connection", err)
	}
}

type closedFetches struct{ jetstream.Consumer }

func (closedFetches) Fetch(int, ...jetstream.FetchOpt) (jetstream.MessageBatch, error) {
	return nil, nats.ErrConnectionClosed
}
