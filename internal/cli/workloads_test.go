package cli_test

import (
	"context"
	"testing"

	"github.com/truvity/audit/auth"
	"github.com/truvity/audit/internal/cli"
)

// A mapped workload registers as the source it is mapped to, and one that is
// not mapped registers as nobody — the map is the whole list of who may.
func TestSourceOfPrefersTheMapping(t *testing.T) {
	ws := auth.Workloads{{Subject: "system:serviceaccount:app:api", Source: "app"}}
	source := cli.SourceOf(ws, "ignored")

	mapped := auth.WithPrincipal(context.Background(), auth.Principal{Subject: "system:serviceaccount:app:api"})
	if got := source(mapped); got != "app" {
		t.Errorf("a mapped workload registers as %q, want %q", got, "app")
	}
	other := auth.WithPrincipal(context.Background(), auth.Principal{Subject: "system:serviceaccount:app:other"})
	if got := source(other); got != "" {
		t.Errorf("an unmapped workload registers as %q, want nobody", got)
	}
}

// With no mapping, the installation's one application is the answer for any
// caller it verified — and only for a caller it verified.
func TestSourceOfFallsBackToTheOneApplication(t *testing.T) {
	source := cli.SourceOf(nil, "app")

	verified := auth.WithPrincipal(context.Background(), auth.Principal{Subject: "system:serviceaccount:app:api"})
	if got := source(verified); got != "app" {
		t.Errorf("a verified caller registers as %q, want %q", got, "app")
	}
	if got := source(context.Background()); got != "" {
		t.Errorf("an unverified caller registers as %q, want nobody", got)
	}
}

// Neither configured is not a licence to take the document's word for it.
func TestSourceOfWithoutEitherNamesNobody(t *testing.T) {
	source := cli.SourceOf(nil, "")
	verified := auth.WithPrincipal(context.Background(), auth.Principal{Subject: "system:serviceaccount:app:api"})
	if got := source(verified); got != "" {
		t.Errorf("registered as %q with nothing configured, want nobody", got)
	}
}
