package logsink_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
	"github.com/truvity/audit/sink/logsink"
	"github.com/truvity/audit/sink/sinktest"
)

func TestConforms(t *testing.T) {
	sinktest.Run(t, func(*testing.T) sinktest.Subject {
		var out bytes.Buffer
		s := logsink.New(logsink.Options{Out: &out})
		return sinktest.Subject{
			Sink: s, Durability: sink.Logged, Refuses: true,
			Count: func() int { return strings.Count(out.String(), "\n") },
		}
	})
}

func TestOneJSONLinePerRecord(t *testing.T) {
	var out bytes.Buffer
	s := logsink.New(logsink.Options{Out: &out, Message: func(r *record.Record) string { return "placed " + r.GetTenantId() }})
	batch := sinktest.Records(2)
	if _, err := s.Write(context.Background(), &sink.Request{Records: batch}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d lines, want 2", len(lines))
	}
	var got struct {
		Message string          `json:"MESSAGE"`
		Record  json.RawMessage `json:"AUDIT_RECORD"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatal(err)
	}
	if got.Message != "placed acme" {
		t.Errorf("MESSAGE = %q", got.Message)
	}
	var back record.Record
	if err := record.Unmarshal(got.Record, &back); err != nil || back.GetId() != batch[0].GetId() {
		t.Errorf("the record did not survive the line: %v, id %q", err, back.GetId())
	}
}

func TestMessageDefaultsToTheAction(t *testing.T) {
	var out bytes.Buffer
	if _, err := logsink.New(logsink.Options{Out: &out}).Write(context.Background(), &sink.Request{Records: sinktest.Records(1)}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"MESSAGE":"shop.order.placed"`) {
		t.Errorf("line = %s", out.String())
	}
}

// Concurrent callers share one stream, and a line must never be cut by another.
func TestLinesDoNotInterleave(t *testing.T) {
	var out bytes.Buffer
	s := logsink.New(logsink.Options{Out: &out})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.Write(context.Background(), &sink.Request{Records: sinktest.Records(5)})
		}()
	}
	wg.Wait()
	for _, l := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if !json.Valid([]byte(l)) {
			t.Fatalf("a line is not JSON: %s", l)
		}
	}
}
