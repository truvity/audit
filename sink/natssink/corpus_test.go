package natssink_test

import (
	"context"
	"testing"

	"github.com/truvity/audit/internal/corpus"
	"github.com/truvity/audit/sink"
	"github.com/truvity/audit/sink/natssink"
)

// The whole corpus crosses the stream unchanged: published, stored on the
// stream's disks, consumed and handed on.
func TestTheCorpusCrossesTheStreamUnchanged(t *testing.T) {
	js, s, _ := stream(t)
	p, err := natssink.NewPublisher(js, natssink.Options{Subject: subject})
	if err != nil {
		t.Fatal(err)
	}
	sent := corpus.Records(t)
	if _, err := p.Write(context.Background(), &sink.Request{Records: sent}); err != nil {
		t.Fatal(err)
	}
	into := &sink.Memory{}
	run(t, s, into, nil, func() bool { return distinct(into.Records()) == len(sent) })
	corpus.Same(t, sent, into.Records())
}
