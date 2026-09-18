package query_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/truvity/audit/auth"
	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/internal/query"
)

// The examples in docs/reference/api.md, asked of a real handler.
//
// Nothing read that document before, which is how it could promise snake_case
// JSON for months while the service wrote camelCase. The examples carry "…"
// where a value would be, so this is not a byte comparison: the request is
// decoded strictly — a field the document names and the proto does not have
// fails here — and sent over HTTP as a browser would; the response must carry
// the keys the document shows, and no camelCase key anywhere.
func TestTheReferenceExamplesAgreeWithTheService(t *testing.T) {
	request, response := examples(t)

	var strict auditv1.SearchRequest
	if err := (protojson.UnmarshalOptions{}).Unmarshal(request, &strict); err != nil {
		t.Fatalf("api.md documents a request the proto does not accept: %v", err)
	}

	s, _ := service(t, fullGrant())
	path, handler := query.NewHandler(s, auth.None{As: caller()})
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	search := func(body []byte) []byte {
		t.Helper()
		res, err := http.Post(server.URL+"/audit.v1.QueryService/Search", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		out, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusOK {
			t.Fatalf("refused: %d %s\nrequest: %s", res.StatusCode, out, body)
		}
		return out
	}

	// The documented request, exactly as documented, is accepted. Nothing in
	// the corpus is a failure, so it matches nothing — which is also a
	// finding: an empty list is omitted, as proto3 JSON always omits it.
	if camel := camelCase(t, search(request)); len(camel) > 0 {
		t.Fatalf("camelCase keys %v in the response to the documented request", camel)
	}

	// A request that matches records, so their field names are checked too.
	body := search([]byte(`{"profile": "security", "limit": 10}`))
	if camel := camelCase(t, body); len(camel) > 0 {
		t.Fatalf("the response has camelCase keys %v; api.md promises snake_case:\n%s", camel, body)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range sortedKeys(response) {
		if _, ok := got[key]; !ok {
			t.Errorf("api.md shows %q in a response; the service did not send it:\n%s", key, body)
		}
	}
	items, _ := got["items"].([]any)
	if len(items) == 0 {
		t.Fatal("no records came back, so no record's field names were checked")
	}
	if first, _ := items[0].(map[string]any); first["tenant_id"] == nil || first["occurred_at"] == nil {
		t.Fatalf("a record without tenant_id and occurred_at: %v", items[0])
	}
}

// examples reads the request and the response blocks out of api.md, with the
// placeholders a reader understands replaced by something JSON does.
func examples(t *testing.T) (request []byte, response map[string]any) {
	t.Helper()
	doc, err := os.ReadFile("../../docs/reference/api.md")
	if err != nil {
		t.Fatal(err)
	}
	blocks := regexp.MustCompile("(?s)```json\n(.*?)```").FindAllSubmatch(doc, -1)
	if len(blocks) < 2 {
		t.Fatalf("api.md has %d JSON examples; this test reads the Search request and its response", len(blocks))
	}
	// In the request the placeholder stands for an actor, and any string
	// will do: it is the shape being checked.
	request = bytes.ReplaceAll(blocks[0][1], []byte(`"…"`), []byte(`"someone"`))
	cleaned := regexp.MustCompile(`\[ … \]`).ReplaceAll(blocks[1][1], []byte(`[]`))
	cleaned = bytes.ReplaceAll(cleaned, []byte(`"…"`), []byte(`""`))
	if err := json.Unmarshal(cleaned, &response); err != nil {
		t.Fatalf("the documented response is not JSON even with its placeholders filled: %v\n%s", err, cleaned)
	}
	return request, response
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func camelCase(t *testing.T, raw []byte) []string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				if strings.ToLower(k) != k {
					out = append(out, k)
				}
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(v)
	return out
}
