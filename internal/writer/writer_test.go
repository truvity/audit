package writer_test

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/internal/writer"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
	"github.com/truvity/audit/store/storetest"
)

type built struct {
	writer     *writer.Writer
	store      *storetest.Memory
	deadLetter []string
	duplicates int
	unhandled  map[string][]string
}

func build(t *testing.T) *built {
	t.Helper()
	c, err := catalogue.Load([]byte(walletDoc), [][]byte{[]byte(walletSchema)})
	if err != nil {
		t.Fatal(err)
	}
	registry := &writer.Registry{}
	registry.Register(c)

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
	b := &built{store: s, unhandled: map[string][]string{}}

	w, err := writer.New(&writer.Writer{
		Catalogues: registry,
		Splitter:   &writer.Splitter{Profiles: profiles(t), Keys: provider},
		Roller:     &writer.Roller{Store: s, Instance: "writer-1", Now: func() time.Time { return at }},
		DeadLetter: &writer.StoreDeadLetter{Store: s, Instance: "writer-1", Now: func() time.Time { return at }},
		Identity:   func(context.Context) string { return "workload:wallet" },
		Now:        func() time.Time { return at },
		Hooks: writer.Hooks{
			OnDeadLettered: func(_ *record.Record, reason string) { b.deadLetter = append(b.deadLetter, reason) },
			OnDuplicate:    func(*record.Record) { b.duplicates++ },
			OnUnhandled:    func(action string, p []string) { b.unhandled[action] = p },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close(context.Background()) })
	b.writer = w
	return b
}

func fresh(t *testing.T) *record.Record {
	t.Helper()
	r := issued(t)
	r.Id = record.NewID()
	r.RecordedAt, r.OriginHash, r.Profile = nil, "", ""
	r.Observer = &record.Observer{Version: "1.2.0", Instance: "wallet-7"}
	return r
}

func write(t *testing.T, b *built, records ...*record.Record) *sink.Result {
	t.Helper()
	res, err := b.writer.Write(context.Background(), &sink.Request{Records: records, Delivery: sink.Block})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	return res
}

// Nothing is acknowledged before it is in the archive. Holding a batch open and
// telling the caller it is safe is the one way this component could lose a
// record while reporting success.
func TestWriteDoesNotReturnBeforeTheObjectIsWritten(t *testing.T) {
	b := build(t)
	res := write(t, b, fresh(t))
	if res.Accepted != 1 {
		t.Fatalf("accepted %d", res.Accepted)
	}
	if b.store.Len() == 0 {
		t.Fatal("the call returned with nothing in the archive")
	}
}

// An emitter's account of who it is is not evidence. The writer stamps the
// identity the transport verified, and the emitter's own version and instance
// are kept as what they are.
func TestWriteStampsTheVerifiedIdentity(t *testing.T) {
	b := build(t)
	r := fresh(t)
	r.Observer.Id = "workload:something-else"
	write(t, b, r)

	for _, c := range decode(t, b.store) {
		if c.GetProfile() == "billing" {
			continue // billing keeps no observer
		}
		if got := c.GetObserver().GetId(); got != "workload:wallet" {
			t.Fatalf("observer id = %q, want the verified identity", got)
		}
		if got := c.GetObserver().GetInstance(); got != "wallet-7" {
			t.Fatalf("observer instance = %q, want the emitter's own", got)
		}
	}
}

// Copies of one record share an identifier and an origin hash and nothing else
// that names a person.
func TestWriteProducesOneCopyPerProfile(t *testing.T) {
	b := build(t)
	r := fresh(t)
	write(t, b, r)

	copies := decode(t, b.store)
	if len(copies) != 3 {
		t.Fatalf("wrote %d copies, want one per configured profile", len(copies))
	}
	hash := ""
	for _, c := range copies {
		if c.GetId() != r.GetId() {
			t.Fatalf("a copy has identifier %q, want %q", c.GetId(), r.GetId())
		}
		if c.GetOriginHash() == "" {
			t.Fatalf("the %s copy has no origin hash", c.GetProfile())
		}
		if hash == "" {
			hash = c.GetOriginHash()
		} else if c.GetOriginHash() != hash {
			t.Fatal("copies of one record carry different origin hashes")
		}
		if c.GetRecordedAt() == nil && c.GetProfile() != "history" {
			t.Fatalf("the %s copy was not stamped with a recorded time", c.GetProfile())
		}
	}
}

// Every hop below the writer is at-least-once on purpose. This is where the
// repeats stop, and it is why none of them has to be careful.
func TestWriteAbsorbsARepeat(t *testing.T) {
	b := build(t)
	r := fresh(t)

	write(t, b, r)
	first := b.store.Len()
	for i := 0; i < 3; i++ {
		res := write(t, b, r)
		if res.Accepted != 1 {
			t.Fatalf("a repeat must be accepted, not refused: %+v", res)
		}
	}
	if b.store.Len() != first {
		t.Fatalf("a repeat was written again: %d objects, was %d", b.store.Len(), first)
	}
	if b.duplicates != 3 {
		t.Fatalf("the duplicate hook fired %d times", b.duplicates)
	}
}

// A fault upstream must not vanish while somebody works out what it was.
func TestWriteDeadLettersRatherThanDropping(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*record.Record)
		want string
	}{
		{"a catalogue nobody registered", func(r *record.Record) {
			r.CatalogueVersion = "9.9.9"
		}, "no catalogue"},
		{"an action the catalogue does not declare", func(r *record.Record) {
			r.Action = "wallet.credential.eaten"
		}, "declares no action"},
		{"a record that does not satisfy its schema", func(r *record.Record) {
			r.Actor.Kind = "ghost"
		}, "not declared"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := build(t)
			r := fresh(t)
			tc.edit(r)
			res := write(t, b, r)
			if res.Accepted != 1 {
				t.Fatalf("a dead-lettered record is still accounted for: %+v", res)
			}
			if len(b.deadLetter) != 1 || !strings.Contains(b.deadLetter[0], tc.want) {
				t.Fatalf("dead letters = %v, want one mentioning %q", b.deadLetter, tc.want)
			}
			var found bool
			for _, key := range b.store.Keys() {
				if strings.HasPrefix(key, "dlq/") {
					found = true
					body, err := b.store.Get(context.Background(), key)
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(string(body), r.GetId()) {
						t.Fatal("the dead letter does not carry the record")
					}
				}
			}
			if !found {
				t.Fatal("nothing was written to the dead-letter prefix")
			}
		})
	}
}

// An action whose profiles this deployment has none of has nowhere to go, and
// must not disappear on the way there.
func TestWriteDeadLettersWhatNoProfileKeeps(t *testing.T) {
	c, err := catalogue.Load([]byte(strings.Replace(walletDoc,
		"profiles: [security, billing, history, nowhere]", "profiles: [nowhere]", 1)),
		[][]byte{[]byte(walletSchema)})
	if err != nil {
		t.Fatal(err)
	}
	b := build(t)
	registry := &writer.Registry{}
	registry.Register(c)
	b.writer.Catalogues = registry

	write(t, b, fresh(t))
	if len(b.deadLetter) != 1 || !strings.Contains(b.deadLetter[0], "no configured profile") {
		t.Fatalf("dead letters = %v", b.deadLetter)
	}
}

// A profile nobody configured is worth saying once, not once per record.
func TestUnhandledProfilesAreReportedOncePerAction(t *testing.T) {
	b := build(t)
	for i := 0; i < 5; i++ {
		write(t, b, fresh(t))
	}
	got := b.unhandled["wallet.credential.issued"]
	if len(got) != 1 || got[0] != "nowhere" {
		t.Fatalf("unhandled = %v", got)
	}
}

// A failure of configuration blames configuration: the batch fails and the
// caller retries, rather than the record being blamed and losing its place.
func TestASplitFailureFailsTheBatch(t *testing.T) {
	b := build(t)
	b.writer.Splitter.Keys = nil

	_, err := b.writer.Write(context.Background(), &sink.Request{Records: []*record.Record{fresh(t)}})
	if err == nil || !strings.Contains(err.Error(), "no key provider") {
		t.Fatalf("want the batch to fail with the configuration error, got %v", err)
	}
	if len(b.deadLetter) != 0 {
		t.Fatal("a configuration fault must not be blamed on the record")
	}
}

// A store that will not take an object must not let the caller believe the
// records are safe.
func TestAFailingStoreFailsTheBatch(t *testing.T) {
	b := build(t)
	b.store.FailPut = errors.New("the bucket is unreachable")
	if _, err := b.writer.Write(context.Background(), &sink.Request{
		Records: []*record.Record{fresh(t)},
	}); err == nil {
		t.Fatal("want the store's error")
	}
}

func TestNewChecksItsParts(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*writer.Writer)
		want string
	}{
		{"no catalogues", func(w *writer.Writer) { w.Catalogues = nil }, "catalogue source"},
		{"no splitter", func(w *writer.Writer) { w.Splitter = nil }, "splitter"},
		{"no roller", func(w *writer.Writer) { w.Roller = nil }, "roller"},
		{"no dead letter", func(w *writer.Writer) { w.DeadLetter = nil }, "nothing may be dropped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &writer.Writer{
				Catalogues: &writer.Registry{},
				Splitter:   &writer.Splitter{},
				Roller:     &writer.Roller{Store: storetest.NewMemory()},
				DeadLetter: &writer.StoreDeadLetter{Store: storetest.NewMemory()},
			}
			tc.edit(w)
			if _, err := writer.New(w); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

// decode reads every copy the writer put, from every object.
func decode(t *testing.T, s *storetest.Memory) []*record.Record {
	t.Helper()
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()

	var out []*record.Record
	for _, key := range s.Keys() {
		if strings.HasPrefix(key, "dlq/") {
			continue
		}
		body, err := s.Get(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := decoder.DecodeAll(body, nil)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		for _, line := range strings.Split(strings.TrimRight(string(plain), "\n"), "\n") {
			if line == "" {
				continue
			}
			var r record.Record
			if err := record.Unmarshal([]byte(line), &r); err != nil {
				t.Fatalf("%s: %v", key, err)
			}
			out = append(out, &r)
		}
	}
	return out
}
