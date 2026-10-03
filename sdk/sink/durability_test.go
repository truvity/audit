package sink_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/truvity/audit/sdk/sink"
	"github.com/truvity/audit/sdk/sink/logsink"
	"github.com/truvity/audit/sdk/sink/sinktest"
)

func TestParseDurability(t *testing.T) {
	for in, want := range map[string]sink.Durability{
		"logged": sink.Logged, "QUEUED": sink.Queued, "archived": sink.Archived,
	} {
		if got, err := sink.ParseDurability(in); err != nil || got != want {
			t.Errorf("ParseDurability(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "durable", "unspecified"} {
		if _, err := sink.ParseDurability(in); err == nil {
			t.Errorf("ParseDurability(%q) succeeded", in)
		}
	}
}

// A chain is only as strong as its last hop, and a hop that says nothing
// guarantees nothing.
func TestRequireRefusesAWeakChain(t *testing.T) {
	logged := logsink.New(logsink.Options{Out: io.Discard})
	if err := sink.Require(logged, sink.Logged); err != nil {
		t.Errorf("logged satisfies logged: %v", err)
	}
	for _, least := range []sink.Durability{sink.Queued, sink.Archived} {
		err := sink.Require(logged, least)
		if err == nil || !strings.Contains(err.Error(), "logged at best") {
			t.Errorf("Require(logged, %v) = %v", least, err)
		}
	}
	if err := sink.Require(sink.Discard, sink.Logged); err == nil {
		t.Error("a sink that says nothing satisfied a requirement")
	}
	if err := sink.Require(sink.Func(nil), sink.Logged); err == nil {
		t.Error("a bare function satisfied a requirement")
	}
	if err := sink.Require(nil, sink.Logged); err == nil {
		t.Error("no sink satisfied a requirement")
	}
}

// What a hop reports at run time is held to the requirement as well: a chain
// that declared enough and then answered less is a failure, not a caveat.
func TestGuardRefusesAWeakAcknowledgement(t *testing.T) {
	liar := &declares{best: sink.Archived, says: sink.Logged}
	g, err := sink.Guard(liar, sink.Queued)
	if err != nil {
		t.Fatal(err)
	}
	res, err := g.Write(context.Background(), &sink.Request{Records: sinktest.Records(1)})
	if err == nil || res != nil || !strings.Contains(err.Error(), "logged") {
		t.Fatalf("Write = %+v, %v; want a refusal naming logged", res, err)
	}

	honest := &declares{best: sink.Archived, says: sink.Archived}
	g, _ = sink.Guard(honest, sink.Queued)
	if res, err := g.Write(context.Background(), &sink.Request{Records: sinktest.Records(1)}); err != nil || res.Accepted != 1 {
		t.Fatalf("Write = %+v, %v", res, err)
	}
	if sink.Guarantees(g) != sink.Archived {
		t.Error("a guard hides what it wraps")
	}
	// Nothing was kept for an empty batch to be weak about.
	if _, err := g.Write(context.Background(), &sink.Request{}); err != nil {
		t.Errorf("an empty batch failed the guard: %v", err)
	}

	if _, err := sink.Guard(&declares{best: sink.Logged}, sink.Queued); err == nil {
		t.Error("Guard accepted a chain that cannot give the requirement")
	}

	failing := &declares{best: sink.Archived, err: errors.New("down")}
	g, _ = sink.Guard(failing, sink.Queued)
	if _, err := g.Write(context.Background(), &sink.Request{Records: sinktest.Records(1)}); err == nil || err.Error() != "down" {
		t.Errorf("the sink's own error was replaced: %v", err)
	}
}

type declares struct {
	best, says sink.Durability
	err        error
}

func (d *declares) Guarantees() sink.Durability { return d.best }

func (d *declares) Write(_ context.Context, req *sink.Request) (*sink.Result, error) {
	if d.err != nil {
		return nil, d.err
	}
	return &sink.Result{Accepted: len(req.Records), Durability: d.says}, nil
}

func TestMemoryConforms(t *testing.T) {
	sinktest.Run(t, func(*testing.T) sinktest.Subject {
		m := &sink.Memory{}
		return sinktest.Subject{Sink: m, Durability: sink.Logged, Count: m.Len}
	})
}

// The durability crosses the wire with the rest of the answer, and the client
// and handler together are a sink like any other.
