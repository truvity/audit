package recobj_test

import (
	"strings"
	"testing"

	"github.com/truvity/audit/internal/recobj"
	"github.com/truvity/audit/sdk/record"
)

func line(t *testing.T, id string) []byte {
	t.Helper()
	canonical, err := record.Canonical(&record.Record{Id: id, Action: "a.b.c", TenantId: "acme", Profile: "security"})
	if err != nil {
		t.Fatal(err)
	}
	return recobj.EncodeLine(canonical)
}

func TestALineIsTheEnvelopeOfTheContract(t *testing.T) {
	l := string(line(t, "one"))
	if !strings.HasPrefix(l, `{"hash":"`) || !strings.Contains(l, `","record":{`) || !strings.HasSuffix(l, "}") {
		t.Fatalf("line = %s", l)
	}
	if strings.ContainsAny(l, "\n ") {
		t.Fatalf("a line is compact and on one line: %q", l)
	}
}

func TestAnObjectRoundTripsAndItsMetadataIsTrue(t *testing.T) {
	body, meta := recobj.Encode([][]byte{line(t, "one"), line(t, "two")})
	lines, err := recobj.Decode(body)
	if err != nil || len(lines) != 2 {
		t.Fatalf("decoded %d lines, %v", len(lines), err)
	}
	for _, l := range lines {
		if err := l.Verify(); err != nil {
			t.Fatal(err)
		}
	}
	if meta["format"] != "1" || meta["count"] != "2" || meta["sha256"] != recobj.SHA256(body) {
		t.Fatalf("metadata = %v", meta)
	}
	if problems := recobj.CheckMetadata(meta, body, 2); len(problems) != 0 {
		t.Fatalf("problems on a true object: %v", problems)
	}
	if problems := recobj.CheckMetadata(map[string]string{"format": "2", "count": "1"}, body, 2); len(problems) != 3 {
		t.Fatalf("problems = %v, want one per wrong key", problems)
	}
}

// A line written by another implementation, with other whitespace and key
// order, still checks: the hash is over the canonical form.
func TestAReaderChecksTheCanonicalFormNotTheBytes(t *testing.T) {
	l, err := recobj.Decode(func() []byte { b, _ := recobj.Encode([][]byte{line(t, "one")}); return b }())
	if err != nil {
		t.Fatal(err)
	}
	spaced := recobj.Line{Hash: l[0].Hash, Record: []byte(strings.ReplaceAll(string(l[0].Record), ",", " , "))}
	if err := spaced.Verify(); err != nil {
		t.Fatalf("the same record with other whitespace: %v", err)
	}
	changed := recobj.Line{Hash: l[0].Hash, Record: []byte(strings.Replace(string(l[0].Record), "acme", "evil", 1))}
	if err := changed.Verify(); err == nil {
		t.Fatal("a changed record passed")
	}
}

func TestWhatIsNotAnObjectIsRefused(t *testing.T) {
	enc := func(plain string) []byte {
		// A body that is valid zstd of the given text.
		body, _ := recobj.Encode([][]byte{[]byte(plain)})
		return body
	}
	for name, body := range map[string][]byte{
		"not zstd":               []byte("plain"),
		"no hash":                enc(`{"record":{}}`),
		"an unknown field":       enc(`{"hash":"a","record":{},"extra":1}`),
		"two values on one line": enc(`{"hash":"a","record":{}} {"hash":"b","record":{}}`),
		"a bare record":          enc(`{"id":"x"}`),
	} {
		if _, err := recobj.Decode(body); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
