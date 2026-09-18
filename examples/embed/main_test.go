package main

import (
	"bytes"
	"context"
	"errors"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/gen/audit/v1/auditv1connect"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/store/storetest"
)

// The point of this example is what a module outside this repository can do,
// and Go lets a package inside it import internal/ freely. So the rule is
// checked: every import of the example's own code is public.
func TestTheExampleImportsOnlyPublicPackages(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range f.Imports {
		path, _ := strconv.Unquote(spec.Path.Value)
		if strings.Contains(path, "/internal/") {
			t.Errorf("the example imports %s, which no other module can", path)
		}
	}
}

// The whole embedded trail, as the application runs it: an order recorded in
// process lands in the archive, the console reads it back through the query
// API as a signed-in operator, and that read is itself in the trail.
func TestAnEmbeddedTrailWritesAndReadsBack(t *testing.T) {
	ctx := context.Background()
	archive := storetest.NewMemory()
	provider, err := keys.NewLocal(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	app, err := Build(ctx, archive, provider)
	if err != nil {
		t.Fatal(err)
	}

	data, _ := structpb.NewStruct(map[string]any{"items": 2, "channel": "web"})
	order := record.NewID()
	if err := app.Emitter.Record(ctx, &record.Record{
		Action:    "shop.order.placed",
		Operation: auditv1.Operation_OPERATION_CREATE,
		TenantId:  "acme",
		Actor:     &record.Actor{Kind: "customer", Id: "alice"},
		Targets:   []*record.Target{{Type: "order", Id: order}},
		Outcome:   &record.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS},
		Data:      data,
	}); err != nil {
		t.Fatalf("recording an order: %v", err)
	}

	server := httptest.NewServer(app.Handler)
	defer server.Close()
	client := auditv1connect.NewQueryServiceClient(http.DefaultClient, server.URL, connect.WithProtoJSON())
	search := func(role string) (*connect.Response[auditv1.SearchResponse], error) {
		req := connect.NewRequest(&auditv1.SearchRequest{Profile: "security", Limit: 10})
		req.Header().Set("X-Signed-In-User", "olga")
		if role != "" {
			req.Header().Set("X-Signed-In-Role", role)
		}
		return client.Search(ctx, req)
	}

	page, err := search("operator")
	if err != nil {
		t.Fatalf("an operator's search: %v", err)
	}
	found := false
	for _, item := range page.Msg.GetItems() {
		if item.GetAction() == "shop.order.placed" && item.GetTargets()[0].GetId() == order {
			found = true
			// The security profile pseudonymises an external actor on the way in.
			if !keys.IsPseudonym(item.GetActor().GetId()) {
				t.Errorf("the security copy carries the customer as %q", item.GetActor().GetId())
			}
			if item.GetObserver().GetId() != "workload:shop" {
				t.Errorf("recorded in process, observed by %q", item.GetObserver().GetId())
			}
		}
	}
	if !found {
		t.Fatalf("the order is not in what the console reads: %v", page.Msg.GetItems())
	}

	if _, err := search(""); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a signed-in user without a grant: %v", err)
	}
	unsigned := connect.NewRequest(&auditv1.SearchRequest{Profile: "security"})
	if _, err := client.Search(ctx, unsigned); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("nobody signed in: %v", err)
	}

	if err := app.Close(ctx); err != nil {
		t.Fatal(err)
	}
	actions := map[string]int{}
	for _, r := range archived(t, archive) {
		actions[r.GetAction()]++
	}
	if actions["audit.search"] == 0 {
		t.Fatalf("the console's read is not in the trail: %v", actions)
	}
	if actions["audit.writer.started"] == 0 || actions["audit.writer.stopped"] == 0 {
		t.Fatalf("the embedded writer kept no account of itself: %v", actions)
	}
}

// archived decodes every record copy in the archive.
func archived(t *testing.T, s *storetest.Memory) []*auditv1.Record {
	t.Helper()
	var out []*auditv1.Record
	for _, key := range s.Keys() {
		if !strings.HasPrefix(key, "profile=") || !strings.HasSuffix(key, ".ndjson.zst") {
			continue
		}
		o, _ := s.Object(key)
		dec, err := zstd.NewReader(bytes.NewReader(o.Body))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(dec)
		dec.Close()
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
			var r auditv1.Record
			if err := protojson.Unmarshal(line, &r); err != nil {
				t.Fatal(errors.Join(err, errors.New(string(line))))
			}
			out = append(out, &r)
		}
	}
	return out
}
