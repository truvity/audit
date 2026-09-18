package query_test

import (
	"strings"
	"testing"

	"github.com/truvity/audit/auth"
	"github.com/truvity/audit/index"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/query"
	"github.com/truvity/audit/sink"
)

// A query service missing a part refuses to start: one that did not know who
// was asking, or recorded no reads, or answered without grants, would be the
// failure it exists to prevent.
func TestNewRefusesAnIncompleteService(t *testing.T) {
	provider, err := keys.NewLocal(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	anyone := auth.AuthenticatorFunc(nil)
	full := query.Config{
		Searcher: index.NewMemory(), Authenticator: anyone,
		Authorizer: auth.Declarative{}, Sink: &sink.Memory{},
	}
	for name, c := range map[string]struct {
		config query.Config
		says   string
	}{
		"no authenticator": {query.Config{Searcher: full.Searcher, Authorizer: full.Authorizer, Sink: full.Sink}, "authenticator"},
		"no sink":          {query.Config{Searcher: full.Searcher, Authorizer: full.Authorizer, Authenticator: anyone}, "sink"},
		"no authorizer":    {query.Config{Searcher: full.Searcher, Authenticator: anyone, Sink: full.Sink}, "authorizer"},
		"resolve without the archive": {query.Config{
			Searcher: full.Searcher, Authenticator: anyone, Authorizer: full.Authorizer, Sink: full.Sink, Keys: provider,
		}, "archive"},
		"exports with nowhere to sign links": {query.Config{
			Searcher: full.Searcher, Authenticator: anyone, Authorizer: full.Authorizer, Sink: full.Sink,
			Exports: &query.Exports{},
		}, "sign links"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := query.New(c.config); err == nil || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("got %v, want a refusal naming %q", err, c.says)
			}
		})
	}
	s, err := query.New(full)
	if err != nil {
		t.Fatalf("a complete service: %v", err)
	}
	if path, h := s.Handler(); path == "" || h == nil {
		t.Fatal("no handler")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
