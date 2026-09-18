package writer_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/internal/writer"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/store/storetest"
)

// start is one start of a writer on an archive that outlives it, which is what
// a restart is.
func start(t *testing.T, s *storetest.Memory, profiles map[string]*preset.Profile, versions map[string]string) error {
	t.Helper()
	common, err := catalogue.Common()
	if err != nil {
		t.Fatal(err)
	}
	registry := &writer.Registry{}
	registry.Register(common)
	root := make([]byte, 32)
	if _, err := rand.Read(root); err != nil {
		t.Fatal(err)
	}
	provider, err := keys.NewLocal(root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	at := day(t, "2026-09-17T10:30:00Z")
	w, err := writer.New(&writer.Writer{
		Catalogues: registry,
		Splitter:   &writer.Splitter{Profiles: profiles, Keys: provider},
		Roller:     &writer.Roller{Store: s, Instance: "writer-1", Now: func() time.Time { return at }},
		DeadLetter: &writer.StoreDeadLetter{Store: s, Instance: "writer-1", Now: func() time.Time { return at }},
		Archive:    &writer.SchemaArchive{Store: s, Now: func() time.Time { return at }},
		Meta:       common,
		Version:    "1.0.0",
		Now:        func() time.Time { return at },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close(context.Background()) }()
	return w.RecordCompositions(context.Background(), profiles, versions)
}

func versions() map[string]string {
	return map[string]string{"security": "2026.09", "billing-nl": "2026.09", "history": "2026.09"}
}

// compositions is every composition object recorded for a profile, in order.
func compositions(t *testing.T, s *storetest.Memory, profile string) []writer.Composition {
	t.Helper()
	var out []writer.Composition
	for _, key := range s.Keys() {
		if !strings.HasPrefix(key, "schema/profile/"+profile+"/") {
			continue
		}
		body, err := s.Get(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		var c writer.Composition
		if err := json.Unmarshal(body, &c); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

// recorded is the writer's own records of one action, from the profile copies.
func recorded(t *testing.T, s *storetest.Memory, action string) []*record.Record {
	t.Helper()
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()
	var out []*record.Record
	for _, key := range s.Keys() {
		if !strings.HasPrefix(key, "profile=security/") {
			continue
		}
		body, _ := s.Get(context.Background(), key)
		plain, err := decoder.DecodeAll(body, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(plain)), "\n") {
			var r record.Record
			if err := record.Unmarshal([]byte(line), &r); err != nil {
				t.Fatal(err)
			}
			if r.GetAction() == action {
				out = append(out, &r)
			}
		}
	}
	return out
}

// The first composition is recorded without claiming a change, and a restart
// with the same profiles records nothing at all.
func TestTheFirstCompositionIsNotAChangeAndASecondStartWritesNothing(t *testing.T) {
	s := storetest.NewMemory()
	for i := 0; i < 2; i++ {
		if err := start(t, s, profiles(t), versions()); err != nil {
			t.Fatal(err)
		}
	}
	if got := compositions(t, s, "security"); len(got) != 1 || got[0].Sequence != 1 {
		t.Fatalf("compositions: %+v", got)
	}
	if got := recorded(t, s, "audit.profile.changed"); len(got) != 0 {
		t.Fatalf("a first start claimed %d change(s)", len(got))
	}
}

// A retention change is recorded with both fingerprints, and the composition
// in force before stays readable beside the new one.
func TestAChangedProfileIsRecordedAndTheOldCompositionKept(t *testing.T) {
	s := storetest.NewMemory()
	if err := start(t, s, profiles(t), versions()); err != nil {
		t.Fatal(err)
	}
	changed := profiles(t)
	longer := *changed["security"]
	longer.Retention.Days += 365
	changed["security"] = &longer
	if err := start(t, s, changed, versions()); err != nil {
		t.Fatal(err)
	}

	got := compositions(t, s, "security")
	if len(got) != 2 || got[0].Sequence != 1 || got[1].Sequence != 2 {
		t.Fatalf("compositions: %d", len(got))
	}
	if got[0].Composed.Retention.Days == got[1].Composed.Retention.Days {
		t.Fatal("the recorded compositions do not show the change")
	}
	events := recorded(t, s, "audit.profile.changed")
	if len(events) != 1 {
		t.Fatalf("%d profile.changed events", len(events))
	}
	data := events[0].GetData().AsMap()
	if data["previous_fingerprint"] != got[0].Fingerprint || data["fingerprint"] != got[1].Fingerprint {
		t.Fatalf("the event names %v", data)
	}
	if events[0].GetTargets()[0].GetId() != "security" {
		t.Fatalf("target %v", events[0].GetTargets())
	}
	// Only the profile that changed.
	if got := compositions(t, s, "history"); len(got) != 1 {
		t.Fatalf("an unchanged profile recorded %d compositions", len(got))
	}
}

// A new preset version is a change of meaning nobody typed, and is recorded as
// such even when the rules it composes to are the same.
func TestAPresetVersionBumpIsRecorded(t *testing.T) {
	s := storetest.NewMemory()
	if err := start(t, s, profiles(t), versions()); err != nil {
		t.Fatal(err)
	}
	bumped := versions()
	bumped["security"] = "2027.01"
	if err := start(t, s, profiles(t), bumped); err != nil {
		t.Fatal(err)
	}
	events := recorded(t, s, "audit.preset.changed")
	if len(events) != 1 {
		t.Fatalf("%d preset.changed events", len(events))
	}
	data := events[0].GetData().AsMap()
	if events[0].GetTargets()[0].GetId() != "security" || data["version"] != "2027.01" || data["previous_version"] != "2026.09" {
		t.Fatalf("event %v %v", events[0].GetTargets(), data)
	}
	if got := recorded(t, s, "audit.profile.changed"); len(got) != 0 {
		t.Fatalf("the rules did not change, yet %d profile.changed events", len(got))
	}
}

// These actions are declared block: a change the trail cannot take stops the
// writer rather than letting it write under rules nothing mentions.
func TestAChangeTheTrailCannotTakeStopsTheWriter(t *testing.T) {
	s := storetest.NewMemory()
	if err := start(t, s, profiles(t), versions()); err != nil {
		t.Fatal(err)
	}
	changed := profiles(t)
	longer := *changed["security"]
	longer.Retention.Days += 365
	changed["security"] = &longer

	s.FailPut = errFull
	if err := start(t, s, changed, versions()); err == nil {
		t.Fatal("a writer started under a change it could not record")
	}
	s.FailPut = nil
	// Nothing was half-recorded: the next start sees the change and records it.
	if err := start(t, s, changed, versions()); err != nil {
		t.Fatal(err)
	}
	if got := compositions(t, s, "security"); len(got) != 2 {
		t.Fatalf("compositions after recovery: %d", len(got))
	}
}

var errFull = errorString("the bucket refuses every write")

type errorString string

func (e errorString) Error() string { return string(e) }
