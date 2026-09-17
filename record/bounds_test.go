package record

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"
)

func TestNormaliseStripsControlCharacters(t *testing.T) {
	r := sample(t)
	r.Outcome.Reason = "refused\nAUDIT: forged line"
	r.Actor.Id = "alice" + string(rune(0))
	Normalise(r, Default)

	if strings.ContainsAny(r.GetOutcome().GetReason(), "\r\n") {
		t.Fatalf("a reason may not carry a line break: %q", r.GetOutcome().GetReason())
	}
	if r.GetActor().GetId() != "alice" {
		t.Fatalf("a control character survived in an identifier: %q", r.GetActor().GetId())
	}
}

func TestNormaliseCapsCountsDeterministically(t *testing.T) {
	b := Default
	b.AttributeKeys = 3

	first := normalisedKeys(t, b)
	for i := 0; i < 20; i++ {
		if got := normalisedKeys(t, b); got != first {
			t.Fatalf("attribute capping is not deterministic: %q then %q", first, got)
		}
	}
	if n := len(strings.Split(first, ",")); n != b.AttributeKeys {
		t.Fatalf("kept %d attributes, want %d", n, b.AttributeKeys)
	}
}

func normalisedKeys(t *testing.T, b Bounds) string {
	t.Helper()
	r := sample(t)
	r.Attributes = map[string]string{"e": "1", "a": "2", "d": "3", "b": "4", "c": "5"}
	Normalise(r, b)
	keys := make([]string, 0, len(r.GetAttributes()))
	for k := range r.GetAttributes() {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return strings.Join(keys, ",")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func TestNormaliseSanitisesAttributeKeys(t *testing.T) {
	r := sample(t)
	r.Attributes = map[string]string{"spaced key": "1", "ok.key-2:x": "2"}
	Normalise(r, Default)
	if _, ok := r.GetAttributes()["spaced_key"]; !ok {
		t.Fatalf("an unsafe key was not sanitised: %v", r.GetAttributes())
	}
	if _, ok := r.GetAttributes()["ok.key-2:x"]; !ok {
		t.Fatalf("a safe key was changed: %v", r.GetAttributes())
	}
}

// One oversized body must not cost the record everything else it carries.
func TestNormaliseDropsAnOversizedBodyBeforeAnythingElse(t *testing.T) {
	r := sample(t)
	big, err := structpb.NewStruct(map[string]any{"body": strings.Repeat("x", 4096)})
	if err != nil {
		t.Fatal(err)
	}
	small, err := structpb.NewStruct(map[string]any{"body": "ok"})
	if err != nil {
		t.Fatal(err)
	}
	r.Capture = &Capture{Request: small, Response: big}

	b := Default
	b.CaptureBytes = 1024
	if !Normalise(r, b) {
		t.Fatal("dropping a body must be reported as truncation")
	}
	if r.GetCapture().GetResponse() != nil {
		t.Fatal("the oversized response should have been dropped")
	}
	if r.GetCapture().GetRequest() == nil {
		t.Fatal("the request was within bounds and should have been kept")
	}
	if !r.GetCapture().GetTruncated() {
		t.Fatal("capture.truncated must say that something is missing")
	}
	if len(r.GetAttributes()) == 0 {
		t.Fatal("attributes should not have been given up for an oversized body")
	}
}

// When the whole record is too large, parts are given up in the published
// order, so a reader knows what is missing without being told.
func TestNormaliseFollowsTheTruncationOrder(t *testing.T) {
	r := sample(t)
	body, err := structpb.NewStruct(map[string]any{"body": strings.Repeat("x", 4096)})
	if err != nil {
		t.Fatal(err)
	}
	r.Capture = &Capture{Request: body, Response: body}
	r.Unmapped = body

	b := Default
	b.CaptureBytes = 1 << 20 // each body is individually acceptable
	b.MaxBytes = 6000        // the record as a whole is not

	if !Normalise(r, b) {
		t.Fatal("want truncation reported")
	}
	if r.GetCapture().GetResponse() != nil {
		t.Fatal("the response is given up first")
	}
	if Size(r) > b.MaxBytes {
		t.Fatalf("record is still %d bytes, over the %d bound", Size(r), b.MaxBytes)
	}
	// Nothing later in the order is given up while something earlier survives.
	if r.GetUnmapped() == nil && r.GetCapture().GetRequest() != nil {
		t.Fatal("unmapped was given up while the request, which comes first, survived")
	}
}

func TestNormaliseLeavesASmallRecordAlone(t *testing.T) {
	r := sample(t)
	before, err := Canonical(r)
	if err != nil {
		t.Fatal(err)
	}
	if Normalise(r, Default) {
		t.Fatal("a record within bounds must not be reported as truncated")
	}
	after, err := Canonical(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("normalising changed a record that was already well formed:\n%s\n%s", before, after)
	}
}
