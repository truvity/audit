package writer_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/internal/writer"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
	"github.com/truvity/audit/store/storetest"
)

type reporting struct {
	writer  *writer.Writer
	store   *storetest.Memory
	dropped []string
	dead    []string
}

// buildReporting is build() with the writer's account of itself turned on, and
// the common catalogue registered beside the wallet's so that the writer can
// resolve its own records the way it resolves anyone else's.
func buildReporting(t *testing.T) *reporting {
	t.Helper()
	wallet, err := catalogue.Load([]byte(walletDoc), [][]byte{[]byte(walletSchema)})
	if err != nil {
		t.Fatal(err)
	}
	common, err := catalogue.Common()
	if err != nil {
		t.Fatal(err)
	}
	registry := &writer.Registry{}
	registry.Register(wallet)
	registry.Register(common)

	root := make([]byte, 32)
	if _, err := rand.Read(root); err != nil {
		t.Fatal(err)
	}
	provider, err := keys.NewLocal(root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })

	s := storetest.NewMemory()
	at := day(t, "2026-09-17T10:30:00Z")
	b := &reporting{store: s}

	w, err := writer.New(&writer.Writer{
		Catalogues: registry,
		Splitter:   &writer.Splitter{Profiles: profiles(t), Keys: provider},
		Roller:     &writer.Roller{Store: s, Instance: "writer-1", Now: func() time.Time { return at }},
		DeadLetter: &writer.StoreDeadLetter{Store: s, Instance: "writer-1", Now: func() time.Time { return at }},
		Meta:       common,
		Identity:   func(context.Context) string { return "workload:wallet" },
		Version:    "1.0.0",
		Now:        func() time.Time { return at },
		Hooks: writer.Hooks{
			OnDeadLettered: func(_ *record.Record, reason string) { b.dead = append(b.dead, reason) },
			OnMetaDropped:  func(action, reason string) { b.dropped = append(b.dropped, action+": "+reason) },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	b.writer = w
	return b
}

// settle waits for the writer's own best-effort records to come round the loop.
// Closing the emitter is what drains them, and Close does that before flushing.
func (b *reporting) settle(t *testing.T) {
	t.Helper()
	if err := b.writer.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// actions counts every action found in the archive.
func (b *reporting) actions(t *testing.T) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, r := range decode(t, b.store) {
		out[r.GetAction()]++
	}
	return out
}

// feed writes records into the writer under test.
func (b *reporting) feed(t *testing.T, records ...*record.Record) {
	t.Helper()
	if _, err := b.writer.Write(context.Background(),
		&sink.Request{Records: records, Delivery: sink.Block}); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// The writer's account of itself lands in the archive it writes, under the same
// profiles and in the same objects as everything else. A reader who cannot tell
// a quiet hour from a stopped writer cannot conclude anything from silence.
func TestTheWriterRecordsItsOwnLifeInItsOwnArchive(t *testing.T) {
	b := buildReporting(t)
	b.writer.Started(context.Background())
	b.feed(t, fresh(t))
	b.settle(t)

	actions := b.actions(t)
	if actions["audit.writer.started"] != 1 {
		t.Fatalf("the writer did not record that it started: %v", actions)
	}
	if actions["audit.writer.stopped"] != 1 {
		t.Fatalf("the writer did not record that it stopped: %v", actions)
	}
	if len(b.dropped) != 0 {
		t.Fatalf("the writer could not record itself: %v", b.dropped)
	}
}

// The writer stamps its own identity on its own records. There is no transport
// to verify it against, and a claimed identity would be worse than the truth.
func TestTheWritersOwnRecordsCarryItsOwnIdentity(t *testing.T) {
	b := buildReporting(t)
	b.writer.Started(context.Background())
	b.settle(t)

	for _, r := range decode(t, b.store) {
		if r.GetAction() != "audit.writer.started" {
			continue
		}
		if got := r.GetObserver().GetId(); got != "writer-1" {
			t.Fatalf("observer.id = %q, want the writer's own instance", got)
		}
		if r.GetOriginHash() == "" {
			t.Fatal("the writer's own record was not stamped")
		}
		return
	}
	t.Fatal("no started record was written")
}

// A dead letter is reported in the trail, not only in the logs, so that the
// hole is visible to whoever reads the archive rather than only to whoever
// watches the process.
func TestADeadLetterIsAlsoRecorded(t *testing.T) {
	b := buildReporting(t)
	bad := fresh(t)
	bad.CatalogueVersion = "9.9.9"
	b.feed(t, bad)
	b.settle(t)

	actions := b.actions(t)
	if actions["audit.writer.dead_lettered"] != 1 {
		t.Fatalf("the dead letter was not recorded in the trail: %v", actions)
	}
	if len(b.dead) != 1 {
		t.Fatalf("dead letters reported: %v", b.dead)
	}
}

// The loop must not feed itself. A meta-record that cannot be written is kept
// and reported, never emitted about, or one bad meta-record would beget another
// for as long as the process ran.
func TestAFailedMetaRecordIsNotEmittedAbout(t *testing.T) {
	b := buildReporting(t)

	// Make every record of the writer's own source fail at the writer, after
	// the emitter has already accepted it: the catalogue the emitter validates
	// against stays good, and the one the writer resolves goes away.
	unregistered := &writer.Registry{}
	wallet, err := catalogue.Load([]byte(walletDoc), [][]byte{[]byte(walletSchema)})
	if err != nil {
		t.Fatal(err)
	}
	unregistered.Register(wallet)
	b.writer.Catalogues = unregistered

	bad := fresh(t)
	bad.CatalogueVersion = "9.9.9"
	b.feed(t, bad)
	b.settle(t)

	// Three dead letters and no more: the record itself, the one meta-record
	// about it, and the stopped record Close emits. Without the guard the
	// meta-record's own failure would emit a second meta-record, that one a
	// third, and the pile would grow until the process stopped.
	if len(b.dead) != 3 {
		t.Fatalf("want three dead letters, got %d:\n%s", len(b.dead), strings.Join(b.dead, "\n"))
	}
	if got := deadLetterActions(t, b.store)["audit.writer.dead_lettered"]; got != 1 {
		t.Fatalf("%d meta-records about dead letters reached the dead-letter prefix, want 1", got)
	}
}

// deadLetterActions counts the actions of what reached the dead-letter prefix.
func deadLetterActions(t *testing.T, s *storetest.Memory) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, key := range s.Keys() {
		if !strings.HasPrefix(key, "dlq/") {
			continue
		}
		body, err := s.Get(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		out[envelope.Action]++
	}
	return out
}

// A writer told to speak about itself with a catalogue nobody registered would
// dead-letter every one of its own records, and in silence, because a meta dead
// letter is not emitted about. It refuses to start instead.
func TestAnUnregisteredMetaCatalogueIsRefused(t *testing.T) {
	common, err := catalogue.Common()
	if err != nil {
		t.Fatal(err)
	}
	s := storetest.NewMemory()
	_, err = writer.New(&writer.Writer{
		Catalogues: &writer.Registry{},
		Splitter:   splitter(t),
		Roller:     &writer.Roller{Store: s, Instance: "writer-1"},
		DeadLetter: &writer.StoreDeadLetter{Store: s, Instance: "writer-1"},
		Meta:       common,
	})
	if err == nil {
		t.Fatal("a writer whose own catalogue is not registered must not start")
	}
	if !strings.Contains(err.Error(), "must be registered") {
		t.Fatalf("the refusal does not say what to do: %v", err)
	}
}

// Registering a catalogue is a change to what the system will accept, and the
// trail says when it happened beside the copy the archive already keeps.
func TestRegisteringACatalogueIsRecorded(t *testing.T) {
	b := buildReporting(t)
	wallet, err := catalogue.Load([]byte(walletDoc), [][]byte{[]byte(walletSchema)})
	if err != nil {
		t.Fatal(err)
	}
	b.writer.Registered(context.Background(), wallet)
	b.settle(t)

	if b.actions(t)["audit.catalogue.registered"] != 1 {
		t.Fatalf("the registration was not recorded: %v", b.actions(t))
	}
}
