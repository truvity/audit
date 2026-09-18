package sink_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	"github.com/truvity/audit/internal/corpus"
	"github.com/truvity/audit/sink"
)

// The whole corpus crosses each in-process and HTTP hop unchanged. Every field
// a record can carry is in it, so a hop that drops or reshapes one fails here
// rather than in an archive nobody reads for a year.
func TestTheCorpusCrossesEveryHopUnchanged(t *testing.T) {
	sent := corpus.Records(t)

	t.Run("in process", func(t *testing.T) {
		into := &sink.Memory{}
		if _, err := into.Write(context.Background(), &sink.Request{Records: sent, Delivery: sink.Block}); err != nil {
			t.Fatal(err)
		}
		corpus.Same(t, sent, into.Records())
	})

	for name, opts := range map[string][]connect.ClientOption{
		"connect, binary": nil,
		// JSON is what a browser and the viewer speak, and the codec it goes
		// through is this repository's own.
		"connect, JSON": {connect.WithProtoJSON()},
	} {
		t.Run(name, func(t *testing.T) {
			into := &sink.Memory{}
			path, handler := sink.NewHandler(into)
			mux := http.NewServeMux()
			mux.Handle(path, handler)
			server := httptest.NewServer(mux)
			t.Cleanup(server.Close)

			client := sink.NewClient(server.Client(), server.URL, opts...)
			if _, err := client.Write(context.Background(), &sink.Request{Records: sent, Delivery: sink.Block}); err != nil {
				t.Fatal(err)
			}
			corpus.Same(t, sent, into.Records())
		})
	}
}
