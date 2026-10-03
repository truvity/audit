package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"

	"github.com/truvity/audit/sdk/record"
	"github.com/truvity/audit/sdk/sink"
)

// fakeSink is the writer behind the guard: it archives, fails or refuses as the
// test says, and remembers what it was handed.
type fakeSink struct {
	err      error
	rejected []string
	got      []*record.Record
}

func (f *fakeSink) Write(_ context.Context, req *sink.Request) (*sink.Result, error) {
	f.got = append(f.got, req.Records...)
	if f.err != nil {
		return nil, f.err
	}
	res := &sink.Result{Durability: sink.Archived, Accepted: len(req.Records)}
	for _, id := range f.rejected {
		res.Rejected = append(res.Rejected, sink.Rejection{ID: id, Reason: "no"})
	}
	return res, nil
}

type ages struct{ got []time.Duration }

func (a *ages) Received(transport string, age time.Duration) {
	if transport != sink.TransportSQS {
		panic("transport " + transport)
	}
	a.got = append(a.got, age)
}

func message(t *testing.T, msgID string, r *record.Record, sent time.Time) events.SQSMessage {
	t.Helper()
	body, err := record.Canonical(r)
	if err != nil {
		t.Fatal(err)
	}
	m := events.SQSMessage{MessageId: msgID, Body: string(body), Attributes: map[string]string{}}
	if !sent.IsZero() {
		m.Attributes["SentTimestamp"] = strconv.FormatInt(sent.UnixMilli(), 10)
	}
	return m
}

func newRecord() *record.Record {
	return &record.Record{Id: record.NewID(), TenantId: "acme", Action: "x.y"}
}

func newHandler(f *fakeSink) (*handler, *ages) {
	a := &ages{}
	return &handler{
		target: f, age: a, now: func() time.Time { return time.UnixMilli(1_000_000_000) },
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, a
}

func ids(f []events.SQSBatchItemFailure) []string {
	var out []string
	for _, x := range f {
		out = append(out, x.ItemIdentifier)
	}
	return out
}

func TestAnArchivedBatchReportsNoFailuresAndRecordsTheAgeOfEachMessage(t *testing.T) {
	f := &fakeSink{}
	h, a := newHandler(f)
	sent := time.UnixMilli(1_000_000_000 - 12_000)
	res, err := h.Handle(context.Background(), events.SQSEvent{Records: []events.SQSMessage{
		message(t, "m1", newRecord(), sent), message(t, "m2", newRecord(), sent),
	}})
	if err != nil || len(res.BatchItemFailures) != 0 {
		t.Fatalf("got %v, %v; want no failures", res, err)
	}
	if len(f.got) != 2 {
		t.Fatalf("the writer was handed %d records, want 2 in one call", len(f.got))
	}
	if len(a.got) != 2 || a.got[0] != 12*time.Second {
		t.Fatalf("ages = %v, want two of 12s from SentTimestamp", a.got)
	}
}

func TestAMessageWithNoSentTimestampIsNotCountedAsAnAge(t *testing.T) {
	h, a := newHandler(&fakeSink{})
	if _, err := h.Handle(context.Background(), events.SQSEvent{Records: []events.SQSMessage{
		message(t, "m1", newRecord(), time.Time{}),
	}}); err != nil {
		t.Fatal(err)
	}
	if len(a.got) != 0 {
		t.Fatalf("an age was invented: %v", a.got)
	}
}

func TestAFailedWriteReturnsEveryMessageToTheQueue(t *testing.T) {
	f := &fakeSink{err: errors.New("S3 is down")}
	h, _ := newHandler(f)
	res, err := h.Handle(context.Background(), events.SQSEvent{Records: []events.SQSMessage{
		message(t, "m1", newRecord(), time.Time{}),
		{MessageId: "bad", Body: "not a record"},
		message(t, "m2", newRecord(), time.Time{}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := ids(res.BatchItemFailures)
	if len(got) != 3 {
		t.Fatalf("failures = %v, want all three messages, once each", got)
	}
	seen := map[string]bool{}
	for _, id := range got {
		if seen[id] {
			t.Fatalf("%s reported twice: %v", id, got)
		}
		seen[id] = true
	}
}

func TestAPoisonMessageFailsAloneAndTheRestAreArchived(t *testing.T) {
	f := &fakeSink{}
	h, _ := newHandler(f)
	res, err := h.Handle(context.Background(), events.SQSEvent{Records: []events.SQSMessage{
		message(t, "m1", newRecord(), time.Time{}),
		{MessageId: "bad", Body: "{not json"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(res.BatchItemFailures); len(got) != 1 || got[0] != "bad" {
		t.Fatalf("failures = %v, want only the message that is not a record", got)
	}
	if len(f.got) != 1 {
		t.Fatalf("the writer got %d records, want the one that decoded", len(f.got))
	}
}

func TestARecordTheSinkRefusesIsItsMessagesFailureOnly(t *testing.T) {
	r1, r2 := newRecord(), newRecord()
	f := &fakeSink{rejected: []string{r2.Id}}
	h, _ := newHandler(f)
	res, err := h.Handle(context.Background(), events.SQSEvent{Records: []events.SQSMessage{
		message(t, "m1", r1, time.Time{}), message(t, "m2", r2, time.Time{}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(res.BatchItemFailures); len(got) != 1 || got[0] != "m2" {
		t.Fatalf("failures = %v, want m2", got)
	}
}

func TestAnAllPoisonBatchNeverCallsTheWriter(t *testing.T) {
	f := &fakeSink{}
	h, _ := newHandler(f)
	res, _ := h.Handle(context.Background(), events.SQSEvent{Records: []events.SQSMessage{{MessageId: "bad", Body: "x"}}})
	if len(f.got) != 0 || len(res.BatchItemFailures) != 1 {
		t.Fatalf("writer got %d, failures %v", len(f.got), res.BatchItemFailures)
	}
}

func TestTheHoldsAreReadBeforeAndTelemetryIsFlushedAfterEveryInvocation(t *testing.T) {
	var order []string
	h, _ := newHandler(&fakeSink{})
	h.before = func(context.Context) { order = append(order, "before") }
	h.after = func(context.Context) { order = append(order, "after") }
	if _, err := h.Handle(context.Background(), events.SQSEvent{}); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "before" || order[1] != "after" {
		t.Fatalf("order = %v", order)
	}
}

func TestOneLine(t *testing.T) {
	for in, want := range map[string]string{
		"":                      "",
		"plain":                 "plain",
		"a\nb":                  "a b",
		"a\r\nb":                "a  b",
		"bad json\n{\"x\":1}\r": "bad json {\"x\":1} ",
	} {
		if got := oneLine(in); got != want {
			t.Errorf("oneLine(%q) = %q, want %q", in, got, want)
		}
	}
}
