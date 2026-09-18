// Package corpus reads the example records in testdata/records, for tests that
// hold a transport or an encoder to them.
//
// The corpus sets every field a record can carry and every enum value
// (record/corpus_test.go holds it to that), so a hop that passes it unchanged
// has been shown to carry all of a record, not the handful of fields a
// hand-built test record happens to set.
package corpus

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/truvity/audit/record"
)

// dir is testdata/records, found from this file rather than from the test's
// working directory, so that any package's test can read it.
func dir(t testing.TB) string {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("corpus: cannot find the repository")
	}
	return filepath.Join(filepath.Dir(here), "..", "..", "testdata", "records")
}

// Records returns every example record, decoded strictly.
func Records(t testing.TB) []*record.Record {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir(t), "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("corpus: no records")
	}
	out := make([]*record.Record, 0, len(files))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var r record.Record
		if err := (protojson.UnmarshalOptions{}).Unmarshal(raw, &r); err != nil {
			t.Fatalf("corpus: %s: %v", filepath.Base(f), err)
		}
		out = append(out, &r)
	}
	return out
}

// Same fails a test unless every sent record arrived byte-identical in its
// canonical form, keyed by identifier. Arrivals may repeat — every hop is
// at-least-once — but none may be missing or altered.
func Same(t testing.TB, sent, arrived []*record.Record) {
	t.Helper()
	got := map[string]string{}
	for _, r := range arrived {
		line, err := record.Canonical(r)
		if err != nil {
			t.Fatal(err)
		}
		if previous, seen := got[r.GetId()]; seen && previous != string(line) {
			t.Fatalf("%s arrived twice, differently", r.GetId())
		}
		got[r.GetId()] = string(line)
	}
	for _, r := range sent {
		want, err := record.Canonical(r)
		if err != nil {
			t.Fatal(err)
		}
		line, ok := got[r.GetId()]
		switch {
		case !ok:
			t.Errorf("%s never arrived", r.GetId())
		case line != string(want):
			t.Errorf("%s changed on the way:\n sent %s\n  got %s", r.GetId(), want, line)
		}
	}
}
