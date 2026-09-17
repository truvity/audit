package record

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
)

func sample(t *testing.T) *Record {
	t.Helper()
	data, err := structpb.NewStruct(map[string]any{
		"credential_format": "sd-jwt-vc",
		"attestation_bytes": 4096,
		"nested":            map[string]any{"b": 2, "a": 1},
		"list":              []any{"z", "a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &Record{
		Id:               "0199a0f0-1234-7000-8000-00000000abcd",
		OccurredAt:       timestamppb.New(time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)),
		SchemaVersion:    SchemaVersion,
		CatalogueVersion: "1.0.0",
		Source:           "wallet",
		Action:           "wallet.credential.issued",
		Operation:        auditv1.Operation_OPERATION_CREATE,
		Outcome:          &Outcome{Result: auditv1.Outcome_RESULT_SUCCESS},
		TenantId:         "acme",
		Actor:            &Actor{Kind: "service", Id: "issuer"},
		Data:             data,
		Attributes:       map[string]string{"zeta": "1", "alpha": "2"},
	}
}

// The protobuf JSON marshaller varies its whitespace on purpose, so the only
// thing that may ever be written or hashed is the canonical form. If this test
// fails, every digest in every archive is at risk.
func TestCanonicalIsStable(t *testing.T) {
	r := sample(t)
	first, err := Canonical(r)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		again, err := Canonical(r)
		if err != nil {
			t.Fatal(err)
		}
		if string(again) != string(first) {
			t.Fatalf("canonical form varies between calls:\n%s\n%s", first, again)
		}
	}
}

// Go randomises map iteration, and a record carries several maps. Building the
// same record twice must still produce the same bytes.
func TestCanonicalIndependentOfMapOrder(t *testing.T) {
	a, err := Canonical(sample(t))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		b, err := Canonical(sample(t))
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(b) {
			t.Fatalf("canonical form depends on map order:\n%s\n%s", a, b)
		}
	}
}

func TestCanonicalSortsKeysAndOmitsWhitespace(t *testing.T) {
	b, err := Canonical(sample(t))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, `": `) || strings.Contains(s, `, "`) {
		t.Fatalf("canonical form contains whitespace after punctuation: %s", s)
	}
	if keys := topLevelKeys(t, b); !sort.StringsAreSorted(keys) {
		t.Fatalf("top-level keys are not in code-unit order: %v", keys)
	}
	if !strings.Contains(s, `"nested":{"a":1,"b":2}`) {
		t.Fatalf("nested keys are not sorted: %s", s)
	}
	if !strings.Contains(s, `"list":["z","a"]`) {
		t.Fatalf("array order must be preserved: %s", s)
	}
	if !strings.Contains(s, `"schema_version"`) || strings.Contains(s, `"schemaVersion"`) {
		t.Fatalf("JSON must use proto field names: %s", s)
	}
	if !strings.Contains(s, `"OPERATION_CREATE"`) {
		t.Fatalf("enums must be written as names: %s", s)
	}
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("canonical form is not valid JSON: %v", err)
	}
}

func TestCanonicalStringEscaping(t *testing.T) {
	var (
		bell      = string(rune(7))
		backslash = string(rune(92))
	)
	for _, tc := range []struct{ in, want string }{
		{"plain", `"plain"`},
		{"quote\"", `"quote\""`},
		{`back\slash`, `"back\\slash"`},
		{"line\nbreak", `"line\nbreak"`},
		{"tab\there", `"tab\there"`},
		{"bell" + bell, `"bell` + backslash + `u0007"`},
		{"html <&>", `"html <&>"`}, // never HTML-escaped, unlike encoding/json
	} {
		var b strings.Builder
		writeCanonicalString(&b, tc.in)
		if got := b.String(); got != tc.want {
			t.Errorf("escape(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// An audit record carries integers and decimal strings. A number that a float64
// cannot hold exactly is refused rather than silently rounded, because a value
// that changes when it is read back is not evidence.
func TestCanonicalRefusesInexactNumbers(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   any
		ok   bool
	}{
		{"integer", 42, true},
		{"zero", 0, true},
		{"negative", -7, true},
		{"large but exact", float64(int64(1) << 52), true},
		{"fraction", 1.5, false},
		{"beyond exact integers", 1e300, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := structpb.NewStruct(map[string]any{"n": tc.in})
			if err != nil {
				t.Fatal(err)
			}
			_, err = Canonical(s)
			if tc.ok && err != nil {
				t.Fatalf("want accepted, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("want refused, got accepted")
			}
		})
	}
}

// Every profile copy of one record carries the same origin hash, which is what
// lets copies be joined and shown to descend from one original.
func TestOriginHashIgnoresProfileAndItself(t *testing.T) {
	r := sample(t)
	want, err := OriginHash(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != 64 {
		t.Fatalf("origin hash %q is not SHA-256 hex", want)
	}
	security, billing := sample(t), sample(t)
	security.Profile, security.OriginHash = "security", want
	billing.Profile, billing.OriginHash = "billing", want
	for _, c := range []*Record{security, billing} {
		got, err := OriginHash(c)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("profile %q changed the origin hash: %s != %s", c.GetProfile(), got, want)
		}
	}
	changed := sample(t)
	changed.Action = "wallet.credential.revoked"
	other, err := OriginHash(changed)
	if err != nil {
		t.Fatal(err)
	}
	if other == want {
		t.Fatal("a different record must not share an origin hash")
	}
}

func TestUnmarshalAcceptsBothFieldSpellings(t *testing.T) {
	for _, in := range []string{
		`{"schema_version":"1.0","tenant_id":"acme"}`,
		`{"schemaVersion":"1.0","tenantId":"acme"}`,
	} {
		var r Record
		if err := Unmarshal([]byte(in), &r); err != nil {
			t.Fatalf("Unmarshal(%s): %v", in, err)
		}
		if r.GetSchemaVersion() != "1.0" || r.GetTenantId() != "acme" {
			t.Fatalf("Unmarshal(%s) lost fields: %+v", in, &r)
		}
	}
}

func TestLessUTF16(t *testing.T) {
	if !lessUTF16("a", "b") || lessUTF16("b", "a") {
		t.Fatal("ASCII order is wrong")
	}
	if !lessUTF16("a", "aa") {
		t.Fatal("prefix must sort first")
	}
	// A supplementary-plane character is a surrogate pair beginning at U+D83D,
	// so it sorts before U+FFFD in code-unit order even though its UTF-8 bytes
	// sort after. Ordering these two ways would canonicalise one record into
	// two different byte strings.
	replacement, emoji := string(rune(0xFFFD)), string(rune(0x1F600))
	if !lessUTF16(emoji, replacement) {
		t.Fatal("UTF-16 code unit order is wrong for supplementary characters")
	}
	if lessUTF16(replacement, emoji) {
		t.Fatal("UTF-16 code unit order is not antisymmetric")
	}
}

// topLevelKeys returns the object keys of a canonical document in the order
// they were written, which is the order a verifier will read them in.
func topLevelKeys(t *testing.T, b []byte) []string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(string(b)))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("canonical form is not an object: %v %v", tok, err)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}
