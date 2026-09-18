package cli_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/truvity/audit/auth"
	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/emit"
	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/gen/audit/v1/auditv1connect"
	"github.com/truvity/audit/index"
	"github.com/truvity/audit/index/s3scan"
	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/query"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/store"
	"github.com/truvity/audit/store/storetest"
	"github.com/truvity/audit/writer"
)

// deployment is a query service over an archive a writer filled, the way an
// application embedding both runs it, reachable only with the bearer "t". It
// returns the service's URL.
func deployment(t *testing.T, searcher func(store.Store) index.Searcher) string {
	t.Helper()
	ctx := context.Background()
	archive := storetest.NewMemory()
	provider, err := keys.NewLocal(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	d, err := preset.ParseDeployment([]byte("profiles:\n  security:\n    presets: [security]\n"))
	if err != nil {
		t.Fatal(err)
	}
	presets, err := preset.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := d.Compose(presets)
	if err != nil {
		t.Fatal(err)
	}
	shop, err := catalogue.LoadFS(os.DirFS(filepath.Join("..", "..", "examples", "emit", "catalogue")), "shop.yaml")
	if err != nil {
		t.Fatal(err)
	}
	w, err := writer.Open(ctx, writer.Config{
		Archive: archive, Profiles: profiles, Keys: provider,
		Catalogues: []*catalogue.Catalogue{shop}, Self: "workload:shop",
	})
	if err != nil {
		t.Fatal(err)
	}
	emitter, err := emit.New(emit.Options{Source: shop.Source, Catalogue: shop, Sink: w})
	if err != nil {
		t.Fatal(err)
	}
	for i, tenant := range []string{"acme", "acme", "globex", "acme", "globex", "acme", "initech"} {
		data, _ := structpb.NewStruct(map[string]any{"items": i + 1, "channel": "web"})
		if err := emitter.Record(ctx, &record.Record{
			Action:    "shop.order.placed",
			Operation: auditv1.Operation_OPERATION_CREATE,
			TenantId:  tenant,
			Actor:     &record.Actor{Kind: "customer", Id: "alice"},
			Targets:   []*record.Target{{Type: "order", Id: record.NewID()}},
			Outcome:   &record.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS},
			Data:      data,
		}); err != nil {
			t.Fatal(err)
		}
	}

	q, err := query.New(query.Config{
		Searcher: searcher(archive),
		Authenticator: auth.AuthenticatorFunc(func(_ context.Context, r *http.Request) (auth.Principal, error) {
			if r.Header.Get("Authorization") != "Bearer t" {
				return auth.Principal{}, errors.New("not signed in")
			}
			return auth.Principal{Issuer: "test", Subject: "auditor"}, nil
		}),
		Authorizer: auth.Declarative{Rules: []auth.Rule{{
			Name: "auditor",
			Grant: auth.Grant{
				AllTenants: true, Profiles: []string{"security"},
				Operations: []auth.Operation{auth.Search, auth.Get},
			},
		}}},
		Sink:    w,
		Archive: archive,
	})
	if err != nil {
		t.Fatal(err)
	}
	path, handler := q.Handler()
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(func() {
		server.Close()
		_ = q.Close()
		_ = emitter.Close()
		_ = w.Close(context.Background())
	})
	return server.URL
}

// signedIn is a client presenting the bearer "t", read from a file as the
// command reads it.
func signedIn(t *testing.T, url string) auditv1connect.QueryServiceClient {
	t.Helper()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return auditv1connect.NewQueryServiceClient(auth.TokenFile(token), url)
}

func scan(s store.Store) index.Searcher { return &s3scan.Scanner{Store: s} }

// A service that keeps its promises passes every check.
func TestConformanceOfAnHonestService(t *testing.T) {
	var out bytes.Buffer
	failed, err := cli.Conformance{
		Client: signedIn(t, deployment(t, scan)), Profiles: []string{"security"}, PageSize: 3, Out: &out,
	}.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if failed != 0 {
		t.Fatalf("%d checks failed:\n%s", failed, out.String())
	}
	for _, check := range []string{
		"paging returns each record once", "newest first", "get returns what search returned",
		"a filter on tenant", "a cursor is refused with another query", "an unknown id is not found",
	} {
		if !strings.Contains(out.String(), check) {
			t.Errorf("the report does not mention %q:\n%s", check, out.String())
		}
	}
}

// reversed answers every page oldest first: the kind of quiet wrongness a
// second searcher implementation can have, and what the suite is for.
type reversed struct{ index.Searcher }

func (r reversed) Search(ctx context.Context, q index.Query) (index.Page, error) {
	page, err := r.Searcher.Search(ctx, q)
	for i, j := 0, len(page.Rows)-1; i < j; i, j = i+1, j-1 {
		page.Rows[i], page.Rows[j] = page.Rows[j], page.Rows[i]
	}
	return page, err
}

// A service that breaks a promise fails, and the report says which.
func TestConformanceCatchesAServiceThatBreaksItsOrder(t *testing.T) {
	var out bytes.Buffer
	failed, err := cli.Conformance{
		Client:   signedIn(t, deployment(t, func(s store.Store) index.Searcher { return reversed{scan(s)} })),
		Profiles: []string{"security"}, PageSize: 3, Out: &out,
	}.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if failed == 0 || !strings.Contains(out.String(), "INVALID  security  newest first") {
		t.Fatalf("a service answering oldest first passed:\n%s", out.String())
	}
}

// A caller the service refuses has nothing to check: the run fails rather
// than reporting an empty profile as conformant.
func TestConformanceRefusedIsAnError(t *testing.T) {
	anonymous := auditv1connect.NewQueryServiceClient(http.DefaultClient, deployment(t, scan))
	_, err := cli.Conformance{Client: anonymous, Profiles: []string{"security"}, Out: &bytes.Buffer{}}.Run(context.Background())
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("an unauthenticated run: %v", err)
	}
}
