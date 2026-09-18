package wire_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/wire"
)

// CamelCase returns every key under a JSON value that has an upper-case letter
// in it. A snake_case document has none; a protojson default one has one for
// every multi-word field.
func CamelCase(t *testing.T, raw []byte) []string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, raw)
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

func TestTheCodecWritesTheProtosOwnNames(t *testing.T) {
	r := &auditv1.Record{
		Id: "018f0000-0000-7000-8000-00000000000a", TenantId: "acme",
		OccurredAt: timestamppb.New(time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)),
		Actor:      &auditv1.Actor{Kind: "operator", Id: "olga"},
	}
	out, err := wire.Codec{}.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if camel := CamelCase(t, out); len(camel) > 0 {
		t.Fatalf("camelCase keys %v in %s", camel, out)
	}
	if !strings.Contains(string(out), `"occurred_at"`) || !strings.Contains(string(out), `"tenant_id"`) {
		t.Fatalf("snake_case names missing: %s", out)
	}
}

// Reading stays as lenient as connect-go's own codec: either spelling, and
// unknown fields dropped, so a client a schema version ahead is not refused.
func TestTheCodecReadsEitherSpelling(t *testing.T) {
	for _, body := range []string{
		`{"tenant_id": "acme", "occurred_at": "2026-09-18T10:00:00Z"}`,
		`{"tenantId": "acme", "occurredAt": "2026-09-18T10:00:00Z"}`,
		`{"tenant_id": "acme", "a_field_from_the_future": 1}`,
	} {
		var r auditv1.Record
		if err := (wire.Codec{}).Unmarshal([]byte(body), &r); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if r.GetTenantId() != "acme" {
			t.Fatalf("%s: tenant %q", body, r.GetTenantId())
		}
	}
}

func TestTheCodecAnswersToBothJSONNames(t *testing.T) {
	if n := len(wire.HandlerOptions()); n != 2 {
		t.Fatalf("%d options; want one per name Connect asks for JSON by", n)
	}
}
