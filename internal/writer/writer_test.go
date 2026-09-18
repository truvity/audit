package writer_test

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/google/go-cmp/cmp"
	"github.com/truvity/audit/catalogue"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/truvity/audit/index"
	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/internal/writer"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
	"github.com/truvity/audit/store/storetest"
)

type built struct {
	writer     *writer.Writer
	store      *storetest.Memory
	dedupe     *writer.MemoryDedupe
	index      *index.Memory
	deadLetter []string
	duplicates int
	unhandled  map[string][]string
}

// parts lets a test swap in the real index and deduplication store, or share a
// store between two writers. Everything a test leaves out is the in-memory one.
type parts struct {
	store    *storetest.Memory
	indexer  index.Indexer
	dedupe   writer.Dedupe
	instance string
	identity func(context.Context) string
}

func build(t *testing.T) *built {
	t.Helper()
	return buildWith(t, parts{})
}

func buildWith(t *testing.T, p parts) *built {
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

	s := p.store
	if s == nil {
		s = storetest.NewMemory()
	}
	at := fixedDay(t)
	b := &built{
		store:     s,
		dedupe:    &writer.MemoryDedupe{},
		index:     index.NewMemory(),
		unhandled: map[string][]string{},
	}
	indexer, dedupe := index.Indexer(b.index), writer.Dedupe(b.dedupe)
	if p.indexer != nil {
		indexer = p.indexer
	}
	if p.dedupe != nil {
		dedupe = p.dedupe
	}
	// Object keys are deterministic per writer instance and window, so two
	// replicas naming themselves apart is what keeps them from colliding.
	instance := "writer-1"
	if p.instance != "" {
		instance = p.instance
	}

	identity := func(context.Context) string { return "workload:wallet" }
	if p.identity != nil {
		identity = p.identity
	}
	w, err := writer.New(&writer.Writer{
		Catalogues: registry,
		Splitter:   &writer.Splitter{Profiles: profiles(t), Keys: provider},
		Roller: &writer.Roller{
			Store: s, Instance: instance, Indexer: indexer,
			Now: func() time.Time { return at },
		},
		Dedupe:     dedupe,
		DeadLetter: &writer.StoreDeadLetter{Store: s, Instance: instance, Now: func() time.Time { return at }},
		Identity:   identity,
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

// fixedDay is the moment every writer test runs at.
//
// It is also the moment the records happen at. An object is keyed by when its
// records occurred, not by when the writer woke, so a fixture that froze one
// clock and left the other on the wall would put objects under today's date and
// look for them under a date written into the test — which passes until
// midnight and then does not.
func fixedDay(t *testing.T) time.Time {
	t.Helper()
	return day(t, "2026-09-17T10:30:00Z")
}

func fresh(t *testing.T) *record.Record {
	t.Helper()
	r := issued(t)
	r.Id = record.NewID()
	r.OccurredAt = timestamppb.New(fixedDay(t))
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

// Two copies of a record look exactly like two records, so a configuration
// that could produce them is refused at start-up rather than at audit time.
func TestGuardReplicas(t *testing.T) {
	inProcess := &writer.MemoryDedupe{}
	if err := writer.GuardReplicas(1, inProcess); err != nil {
		t.Fatalf("one replica with an in-process table is the case it is for: %v", err)
	}
	err := writer.GuardReplicas(3, inProcess)
	if err == nil {
		t.Fatal("several replicas sharing a stream must not deduplicate in process")
	}
	for _, want := range []string{"written twice", "shared", "run one replica"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should say %q: %v", want, err)
		}
	}
	if err := writer.GuardReplicas(3, shared{}); err != nil {
		t.Fatalf("a shared store is what makes several replicas safe: %v", err)
	}
}

// shared stands in for a deduplication store two replicas both see.
type shared struct{}

func (shared) Seen(context.Context, []string) (map[string]bool, error) { return nil, nil }
func (shared) Mark(context.Context, []string) error                    { return nil }
func (shared) Purge(context.Context, time.Time) error                  { return nil }

// A record is remembered only once its copies are durable. The other order
// reads better and loses records: a crash between remembering and writing would
// leave the identifier marked in a table that survives the crash, and the
// redelivery that would have saved the record would arrive looking like a
// repeat.
func TestARecordIsNotRememberedUntilItIsWritten(t *testing.T) {
	b := build(t)
	b.store.FailPut = errors.New("the bucket is unreachable")
	r := fresh(t)
	if _, err := b.writer.Write(context.Background(), &sink.Request{
		Records: []*record.Record{r},
	}); err == nil {
		t.Fatal("want the store's error")
	}
	if b.dedupe.Len() != 0 {
		t.Fatal("a record that was never written must not be remembered as written")
	}

	// The redelivery has to land, which is the whole point.
	b.store.FailPut = nil
	write(t, b, r)
	if got := len(decode(t, b.store)); got == 0 {
		t.Fatal("the redelivery of a failed write was dropped")
	}
	if b.dedupe.Len() == 0 {
		t.Fatal("a written record must be remembered")
	}
}

// A redelivery bundled with its original is an ordinary shape of batch, and
// asking the store does not settle it: nothing is marked until the batch is
// durable, so the batch keeps its own account.
func TestARepeatWithinOneBatchIsAbsorbed(t *testing.T) {
	b := build(t)
	r := fresh(t)
	result := write(t, b, r, r)
	if result.Accepted != 2 {
		t.Fatalf("accepted %d, want both records answered for", result.Accepted)
	}
	if b.duplicates != 1 {
		t.Fatalf("%d duplicates reported, want 1", b.duplicates)
	}
	var security int
	for _, c := range decode(t, b.store) {
		if c.GetProfile() == "security" {
			security++
		}
	}
	if security != 1 {
		t.Fatalf("%d copies kept by the security profile, want 1", security)
	}
}

// The index is written after the object and says where in it each record is,
// so that an answer can be checked against the copy the digest chain accounts
// for.
func TestWrittenRecordsAreIndexed(t *testing.T) {
	b := build(t)
	r := fresh(t)
	write(t, b, r)

	rows := b.index.Rows("security")
	if len(rows) != 1 {
		t.Fatalf("%d rows indexed, want 1", len(rows))
	}
	row := rows[0]
	if row.ID != r.GetId() || row.Action != r.GetAction() {
		t.Fatalf("the row is not the record: %+v", row)
	}
	if row.ObjectKey == "" || row.Line != 1 {
		t.Fatalf("the row does not say where the copy is: %+v", row)
	}
	if _, found := b.store.Object(row.ObjectKey); !found {
		t.Fatalf("the row points at %q, which is not in the archive", row.ObjectKey)
	}
}

// The index is a projection that a reindex rebuilds. Failing the write when it
// is unreachable would let an outage of the search database stop the audit
// trail, which is the wrong way round.
func TestAnIndexFailureDoesNotFailTheWrite(t *testing.T) {
	b := build(t)
	var deferred int
	b.writer.Roller.Indexer = failingIndex{}
	b.writer.Roller.OnIndexDeferred = func(string, int, error) { deferred++ }

	write(t, b, fresh(t))
	if len(decode(t, b.store)) == 0 {
		t.Fatal("an unreachable index stopped the archive")
	}
	// One report per object, so that a deployment can tell a single unlucky
	// object from an index that has stopped accepting anything.
	if deferred != b.store.Len() {
		t.Fatalf("%d of %d written objects were reported as deferred", deferred, b.store.Len())
	}
}

type failingIndex struct{}

func (failingIndex) Index(context.Context, string, []index.Row) error {
	return errors.New("the index is unreachable")
}

func (failingIndex) Purge(context.Context, string, time.Time, index.Scope) error { return nil }

// The index is a projection, and this is what that claim means: what the writer
// put in the index is exactly what a rebuild from the archive produces. If the
// two ever diverged, the archive would have stopped being the record and the
// database would have quietly become one.
func TestAReindexReproducesWhatTheWriterIndexed(t *testing.T) {
	b := build(t)
	for i := 0; i < 5; i++ {
		write(t, b, fresh(t))
	}

	rebuilt := index.NewMemory()
	day := fixedDay(t)
	report, err := cli.Reindex{
		Store: b.store, Index: rebuilt,
		Fields:  func(context.Context, *record.Record) (index.Fields, error) { return walletFields(), nil },
		Profile: "security", From: day, To: day, Out: io.Discard,
	}.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Records == 0 {
		t.Fatal("the rebuild read nothing")
	}

	was, now := b.index.Rows("security"), rebuilt.Rows("security")
	if len(was) != len(now) {
		t.Fatalf("the writer indexed %d rows and the rebuild produced %d", len(was), len(now))
	}
	for i := range was {
		if diff := cmp.Diff(was[i], now[i]); diff != "" {
			t.Fatalf("row %d differs between the writer and the rebuild (-writer +rebuild):\n%s", i, diff)
		}
	}
	if diff := cmp.Diff(b.index.Counts("security"), rebuilt.Counts("security")); diff != "" {
		t.Fatalf("the facet counts differ (-writer +rebuild):\n%s", diff)
	}
}

// walletFields is what the test catalogue marks indexable, which the writer
// resolved through the catalogue and a rebuild resolves the same way.
func walletFields() index.Fields {
	c, err := catalogue.Load([]byte(walletDoc), [][]byte{[]byte(walletSchema)})
	if err != nil {
		panic(err)
	}
	x, err := c.Compose("wallet.credential.issued")
	if err != nil {
		panic(err)
	}
	if x.Data == nil {
		return index.Fields{}
	}
	return index.Fields{Filter: x.Data.Filterable(), Facet: x.Data.Facets()}
}

// The window a hold exists to close: a hold is placed on a prefix, objects keep
// arriving under it, and one held only by a later sweep was deletable in
// between. The writer sets the hold as it writes.
func TestObjectsWrittenUnderAHeldPrefixCarryIt(t *testing.T) {
	b := buildWith(t, parts{})
	b.writer.Roller.Held = func(profile, tenant string) bool {
		return profile == "security" && tenant == "acme"
	}
	write(t, b, fresh(t))

	held := map[string]bool{}
	for _, key := range b.store.HeldKeys() {
		held[key] = true
	}
	if len(held) == 0 {
		t.Fatal("nothing was written with the hold the prefix is under")
	}
	for _, key := range b.store.Keys() {
		want := strings.HasPrefix(key, "profile=security/tenant=acme/")
		if held[key] != want {
			t.Fatalf("%s: held=%v, want %v", key, held[key], want)
		}
	}
}

// Without a hold nothing is held, which is the ordinary case and must not cost
// anything.
func TestObjectsAreNotHeldWithoutAHold(t *testing.T) {
	b := build(t)
	write(t, b, fresh(t))
	if held := b.store.HeldKeys(); len(held) != 0 {
		t.Fatalf("objects were held with no hold in place: %v", held)
	}
}
