package bucketcontract_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/truvity/audit/internal/bucketcontract"
	"github.com/truvity/audit/internal/recobj"
	"github.com/truvity/audit/internal/s3test"
	"github.com/truvity/audit/internal/ulid"
	"github.com/truvity/audit/internal/writer"
	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/sdk/catalogue"
	auditv1 "github.com/truvity/audit/sdk/gen/audit/v1"
	"github.com/truvity/audit/sdk/record"
	"github.com/truvity/audit/store"
	"github.com/truvity/audit/store/storetest"
)

// The conformance suite of the v1 bucket contract
// (docs/reference/bucket-contract.md).
//
// Every case runs twice: against the in-memory store, which a unit test can
// afford, and against an S3 API (LocalStack in CI, any endpoint named by
// AUDIT_S3_URL), which is the only place the conditional put and the metadata
// are the real ones. The same cases, the same checker, so a rule the memory
// store flatters is a rule the bucket does not.
//
// Two halves. The first writes with the code this repository ships and asks the
// checker whether what it wrote conforms, and checks a few of the rules a
// second way, without the checker's own helpers, so that the checker is not the
// only definition of the format. The second hands the checker objects broken
// one rule at a time, and asks that it names the rule.

// stores are the places the suite runs.
var stores = []struct {
	name string
	open func(*testing.T) store.Store
}{
	{"memory", func(*testing.T) store.Store { return storetest.NewMemory() }},
	{"s3-locked", func(t *testing.T) store.Store { return s3test.Open(t, true) }},
	{"s3-unlocked", func(t *testing.T) store.Store { return s3test.Open(t, false) }},
}

func each(t *testing.T, run func(t *testing.T, s store.Store)) {
	t.Helper()
	for _, st := range stores {
		t.Run(st.name, func(t *testing.T) { run(t, st.open(t)) })
	}
}

// now is far enough from the present that no clock but the test's could have
// made a key from it.
var now = time.Date(2026, 9, 17, 10, 40, 0, 0, time.UTC)

func common(t *testing.T) *catalogue.Catalogue {
	t.Helper()
	c, err := catalogue.Common()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func profiles(t *testing.T) map[string]*preset.Profile {
	t.Helper()
	builtin, err := preset.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*preset.Profile{}
	for name, presets := range map[string][]string{"security": {"security"}, "billing": {"billing-nl"}} {
		p, err := preset.Compose(preset.Composition{Name: name, Presets: presets}, builtin)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = p
	}
	return out
}

var nextID int

// copyOf is one profile's copy of a record of the common catalogue's source.
func copyOf(t *testing.T, profile, tenant string) *record.Record {
	t.Helper()
	c := common(t)
	nextID++
	return &record.Record{
		Id:               fmt.Sprintf("0199b100-0000-7000-8000-%012x", nextID),
		OccurredAt:       timestamppb.New(now.Add(-48 * time.Hour)),
		RecordedAt:       timestamppb.New(now),
		SchemaVersion:    record.SchemaVersion,
		CatalogueVersion: c.Version,
		Source:           c.Source,
		Action:           "audit.writer.started",
		Operation:        auditv1.Operation_OPERATION_CREATE,
		Outcome:          &record.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS},
		TenantId:         tenant,
		Profile:          profile,
		Actor:            &record.Actor{Kind: "system", Id: "writer-1"},
	}
}

// written drives the write path of this repository: the roller, as the writer
// uses it, on a clock the test moves, and the catalogue archive beside it.
// It returns the keys in the order the batches were put.
func written(t *testing.T, s store.Store, held func(profile, tenant string) bool) (keys []string, clock *time.Time) {
	t.Helper()
	ctx := context.Background()
	at := now
	roller := &writer.Roller{
		Store: s, Instance: "writer-1", Now: func() time.Time { return at }, Held: held,
		OnPut: func(key string, _ int) { keys = append(keys, key) },
	}
	t.Cleanup(func() { _ = roller.Close() })
	all := profiles(t)

	archive := &writer.SchemaArchive{
		Store: s, Now: func() time.Time { return at },
		RetainUntil: func(from time.Time) time.Time { return from.AddDate(10, 0, 0) },
	}
	if err := archive.EnsureCatalogue(ctx, common(t)); err != nil {
		t.Fatal(err)
	}

	// Batches over three hours, two profiles and two tenants. A record's own
	// time is two days before: the key is the time of ingest.
	for _, step := range []time.Duration{0, 5 * time.Minute, 30 * time.Minute, 70 * time.Minute} {
		at = at.Add(step)
		for _, c := range []struct{ profile, tenant string }{
			{"security", "acme"}, {"security", "acme"}, {"security", "globex"}, {"billing", "acme"},
		} {
			if err := roller.AddExpiring(ctx, all[c.profile], copyOf(t, c.profile, c.tenant), nil); err != nil {
				t.Fatal(err)
			}
		}
		if err := roller.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return keys, &at
}

// What this repository writes conforms.
func TestWhatTheWriterWritesConforms(t *testing.T) {
	each(t, func(t *testing.T, s store.Store) {
		keys, _ := written(t, s, nil)
		report, err := bucketcontract.Check(context.Background(), s, bucketcontract.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !report.OK() {
			t.Fatalf("the writer's own objects do not conform:\n%s", report)
		}
		// 4 batches x 3 (profile, tenant) partitions; 4 records a batch.
		if report.Objects != len(keys) || report.Objects != 12 || report.Records != 16 || report.Catalogues != 1 {
			t.Fatalf("checked %d objects, %d records, %d catalogues; wrote %d objects", report.Objects, report.Records, report.Catalogues, len(keys))
		}
	})
}

var keyGrammar = regexp.MustCompile(`^records/(security|billing)/(acme|globex)/\d{4}/\d{2}/\d{2}/\d{2}/[0-9A-HJKMNP-TV-Z]{26}$`)

// The rules of the contract, checked a second way: by hand, with nothing of the
// checker's and nothing of the format's own package but the compression.
func TestTheContractByHand(t *testing.T) {
	each(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		keys, _ := written(t, s, nil)
		decoder, err := zstd.NewReader(nil)
		if err != nil {
			t.Fatal(err)
		}
		defer decoder.Close()

		for _, key := range keys {
			if !keyGrammar.MatchString(key) {
				t.Fatalf("%s is not records/<profile>/<tenant>/<yyyy>/<mm>/<dd>/<hh>/<ULID>", key)
			}
			parts := strings.Split(key, "/")
			if parts[3] != "2026" || parts[4] != "09" || parts[5] != "17" && parts[5] != "18" {
				t.Fatalf("%s is not keyed by the day of ingest", key)
			}

			head, err := s.Head(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			body, err := s.Get(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(body)
			if head.Metadata["format"] != "1" || head.Metadata["sha256"] != hex.EncodeToString(sum[:]) {
				t.Fatalf("%s: metadata %v", key, head.Metadata)
			}
			plain, err := decoder.DecodeAll(body, nil)
			if err != nil {
				t.Fatalf("%s: not zstd: %v", key, err)
			}
			lines := strings.Split(strings.TrimSuffix(string(plain), "\n"), "\n")
			if head.Metadata["count"] != fmt.Sprint(len(lines)) {
				t.Fatalf("%s: count %s, %d lines", key, head.Metadata["count"], len(lines))
			}
			for _, line := range lines {
				// {"hash":"<64 hex>","record":{...}} and nothing else.
				const prefix = `{"hash":"`
				if !strings.HasPrefix(line, prefix) || line[len(prefix)+64:len(prefix)+64+len(`","record":`)] != `","record":` || !strings.HasSuffix(line, "}") {
					t.Fatalf("%s: a line is not the envelope: %.120s", key, line)
				}
				hash, rec := line[len(prefix):len(prefix)+64], line[len(prefix)+64+len(`","record":`):len(line)-1]
				canonical, err := record.CanonicalJSON([]byte(rec))
				if err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(canonical)
				if hex.EncodeToString(sum[:]) != hash || string(canonical) != rec {
					t.Fatalf("%s: the hash is not the SHA-256 of the canonical record", key)
				}
			}
		}
	})
}

// Keys sort by ingest time: within a (profile, tenant), a listing returns the
// batches in the order they were put, across hours and within one.
func TestKeysSortByIngestTime(t *testing.T) {
	each(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		keys, _ := written(t, s, nil)

		for _, prefix := range []string{store.TenantPrefix("security", "acme"), store.TenantPrefix("billing", "acme")} {
			var put []string
			for _, k := range keys {
				if strings.HasPrefix(k, prefix) {
					put = append(put, k)
				}
			}
			if len(put) != 4 {
				t.Fatalf("%s: %d batches", prefix, len(put))
			}
			listed, err := s.List(ctx, prefix, "", 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(listed) != len(put) {
				t.Fatalf("%s: listed %d of %d", prefix, len(listed), len(put))
			}
			for i, e := range listed {
				if e.Key != put[i] {
					t.Fatalf("%s: the listing's %dth key is %s, and the %dth batch put was %s", prefix, i+1, e.Key, i+1, put[i])
				}
			}
			// Resuming after a key gives exactly what follows it: the cursor.
			rest, err := s.List(ctx, prefix, put[1], 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(rest) != 2 || rest[0].Key != put[2] {
				t.Fatalf("%s: after %s the listing is %v", prefix, put[1], rest)
			}
		}

		// The walk by hour sees the hours asked for and no others.
		hours := map[string]int{}
		err := store.WalkHours(ctx, s, "security", "acme", now.Truncate(time.Hour).Add(time.Hour), now.Add(48*time.Hour), "",
			func(e store.Entry) error {
				hours[store.HourPrefix("security", "acme", mustParse(t, e.Key).Hour)]++
				return nil
			})
		if err != nil {
			t.Fatal(err)
		}
		if len(hours) != 2 {
			t.Fatalf("the walk saw %v, want hours 11 and 12 and no earlier one", hours)
		}
		for h := range hours {
			if strings.HasSuffix(h, "/10/") {
				t.Fatalf("the walk began before the range: %v", hours)
			}
		}
	})
}

func mustParse(t *testing.T, key string) store.RecordObject {
	t.Helper()
	o, ok := store.ParseRecordKey(key)
	if !ok {
		t.Fatalf("%s does not parse", key)
	}
	return o
}

// A record object is put once: a second put to the key is refused, which on a
// bucket is the conditional put (If-None-Match: *) doing its work.
func TestAnObjectIsWrittenOnce(t *testing.T) {
	each(t, func(t *testing.T, s store.Store) {
		keys, _ := written(t, s, nil)
		err := s.Put(context.Background(), store.Object{
			Key: keys[0], Body: []byte("another"), RetainUntil: now.AddDate(1, 0, 0),
		})
		if !errors.Is(err, store.ErrExists) {
			t.Fatalf("a second put to %s: %v", keys[0], err)
		}
	})
}

// Retention and legal hold are the profile's, set on the put.
func TestRetentionAndHoldAreSetOnThePut(t *testing.T) {
	each(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		keys, _ := written(t, s, func(profile, tenant string) bool { return profile == "security" && tenant == "globex" })
		all := profiles(t)
		for _, key := range keys {
			o := mustParse(t, key)
			head, err := s.Head(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			if head.RetainUntil.IsZero() {
				// An unlocked bucket keeps and reports no lock, and the
				// retention and the hold are then not the contract's to check.
				continue
			}
			// The clock moved a little over an hour between the first batch and
			// the last, and the retention is the profile's from the batch's own
			// ingest: it is within that of what the profile asks of `now`.
			want := all[o.Profile].RetainUntil(now, nil)
			if d := head.RetainUntil.Sub(want); d > 2*time.Hour || d < -2*time.Hour {
				t.Fatalf("%s retained until %s, the profile asks %s", key, head.RetainUntil, want)
			}
			wantHold := o.Profile == "security" && o.Tenant == "globex"
			if head.LegalHold != wantHold {
				t.Fatalf("%s: legal hold %v, want %v", key, head.LegalHold, wantHold)
			}
		}
	})
}

// The catalogue is at catalogue/<app>/<version>, once, as registered; the same
// bytes are success and other bytes are an error.
func TestTheCatalogueIsWrittenOnceAndComparedAfter(t *testing.T) {
	each(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		c := common(t)
		archive := &writer.SchemaArchive{Store: s, RetainUntil: func(from time.Time) time.Time { return from.AddDate(10, 0, 0) }}
		if err := archive.EnsureCatalogue(ctx, c); err != nil {
			t.Fatal(err)
		}
		key := "catalogue/" + c.Source + "/" + c.Version
		held, err := s.Get(ctx, key)
		if err != nil {
			t.Fatalf("the catalogue is not at %s: %v", key, err)
		}
		if string(held) != string(c.Document()) {
			t.Fatal("the catalogue is not the document as registered")
		}

		// Another writer, which has done nothing yet, finds the key present with
		// the same bytes and carries on.
		if err := (&writer.SchemaArchive{Store: s}).EnsureCatalogue(ctx, c); err != nil {
			t.Fatalf("the same bytes under the same version: %v", err)
		}

		// A put with other bytes is refused by the bucket, and the writer then
		// reads the key and refuses to run.
		err = s.Put(ctx, store.Object{Key: key, Body: []byte("other"), RetainUntil: now.AddDate(1, 0, 0)})
		if !errors.Is(err, store.ErrExists) {
			t.Fatalf("a second put with other bytes: %v", err)
		}
		changed, err := catalogue.Load(append(append([]byte{}, c.Document()...), []byte("\n# changed\n")...), nil)
		if err == nil {
			err = (&writer.SchemaArchive{Store: s}).EnsureCatalogue(ctx, changed)
			if !errors.Is(err, writer.ErrCatalogueConflict) {
				t.Fatalf("other bytes under the same version: %v", err)
			}
		}
	})
}

// --- The checker refuses what breaks a rule -----------------------------------------

// object builds a record object's bytes from record lines, with the metadata
// that is true of them.
func object(t *testing.T, profile, tenant string, n int) (body []byte, meta map[string]string) {
	t.Helper()
	var lines [][]byte
	for i := 0; i < n; i++ {
		canonical, err := record.Canonical(copyOf(t, profile, tenant))
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, recobj.EncodeLine(canonical))
	}
	return recobj.Encode(lines)
}

func put(t *testing.T, s store.Store, key string, body []byte, meta map[string]string) {
	t.Helper()
	if err := s.Put(context.Background(), store.Object{
		Key: key, Body: body, Metadata: meta, ContentType: recobj.ContentType, Encoding: recobj.Encoding,
		RetainUntil: time.Now().Add(24 * time.Hour).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
}

func keyAt(profile, tenant string, at time.Time, serial uint64) string {
	return store.RecordKey(profile, tenant, at, ulid.From(at, serial))
}

func without(m map[string]string, drop string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		if k != drop {
			out[k] = v
		}
	}
	return out
}

func with(m map[string]string, k, v string) map[string]string {
	out := without(m, "")
	out[k] = v
	return out
}

func TestTheCheckerNamesTheRuleThatIsBroken(t *testing.T) {
	good := func(t *testing.T) ([]byte, map[string]string) { return object(t, "security", "acme", 2) }
	hour := now.Truncate(time.Hour)
	okKey := keyAt("security", "acme", hour, 1)

	for _, c := range []struct {
		name string
		rule string
		// build writes the broken bucket. The catalogue is archived first, so
		// that only the rule under test is broken.
		build func(t *testing.T, s store.Store)
	}{
		{"an extra component in the key", bucketcontract.RuleKey, func(t *testing.T, s store.Store) {
			body, meta := good(t)
			put(t, s, store.TenantPrefix("security", "acme")+"2026/09/17/10/extra/"+ulid.From(hour, 1), body, meta)
		}},
		{"a ULID from another hour", bucketcontract.RuleKey, func(t *testing.T, s store.Store) {
			body, meta := good(t)
			put(t, s, store.HourPrefix("security", "acme", hour)+ulid.From(hour.Add(time.Hour), 1), body, meta)
		}},
		{"a ULID that is not one", bucketcontract.RuleKey, func(t *testing.T, s store.Store) {
			body, meta := good(t)
			put(t, s, store.HourPrefix("security", "acme", hour)+"not-a-ulid", body, meta)
		}},
		{"no format", bucketcontract.RuleMetadata, func(t *testing.T, s store.Store) {
			body, meta := good(t)
			put(t, s, okKey, body, without(meta, "format"))
		}},
		{"another format", bucketcontract.RuleMetadata, func(t *testing.T, s store.Store) {
			body, meta := good(t)
			put(t, s, okKey, body, with(meta, "format", "2"))
		}},
		{"no sha256", bucketcontract.RuleMetadata, func(t *testing.T, s store.Store) {
			body, meta := good(t)
			put(t, s, okKey, body, without(meta, "sha256"))
		}},
		{"a sha256 that is not the object's", bucketcontract.RuleSHA256, func(t *testing.T, s store.Store) {
			body, meta := good(t)
			put(t, s, okKey, body, with(meta, "sha256", strings.Repeat("0", 64)))
		}},
		{"no count", bucketcontract.RuleMetadata, func(t *testing.T, s store.Store) {
			body, meta := good(t)
			put(t, s, okKey, body, without(meta, "count"))
		}},
		{"a count that is not the lines", bucketcontract.RuleCount, func(t *testing.T, s store.Store) {
			body, meta := good(t)
			put(t, s, okKey, body, with(meta, "count", "5"))
		}},
		{"a body that is not zstd", bucketcontract.RuleBody, func(t *testing.T, s store.Store) {
			put(t, s, okKey, []byte("not zstd"), map[string]string{"format": "1", "sha256": recobj.SHA256([]byte("not zstd")), "count": "1"})
		}},
		{"a line that is not the envelope", bucketcontract.RuleBody, func(t *testing.T, s store.Store) {
			body, meta := recobj.Encode([][]byte{[]byte(`{"record":{}}`)})
			put(t, s, okKey, body, meta)
		}},
		{"a record whose hash is not its own", bucketcontract.RuleHash, func(t *testing.T, s store.Store) {
			canonical, _ := record.Canonical(copyOf(t, "security", "acme"))
			line := []byte(`{"hash":"` + strings.Repeat("a", 64) + `","record":` + string(canonical) + `}`)
			body, meta := recobj.Encode([][]byte{line})
			put(t, s, okKey, body, meta)
		}},
		{"a record under another tenant", bucketcontract.RulePlacement, func(t *testing.T, s store.Store) {
			body, meta := object(t, "security", "globex", 1)
			put(t, s, okKey, body, meta)
		}},
		{"a record under another profile", bucketcontract.RulePlacement, func(t *testing.T, s store.Store) {
			body, meta := object(t, "billing", "acme", 1)
			put(t, s, okKey, body, meta)
		}},
		{"two batches with one ULID", bucketcontract.RuleUnique, func(t *testing.T, s store.Store) {
			body, meta := good(t)
			put(t, s, keyAt("security", "acme", hour, 7), body, meta)
			body, meta = object(t, "security", "globex", 1)
			put(t, s, keyAt("security", "globex", hour, 7), body, meta)
		}},
		{"a catalogue that is not in the bucket", bucketcontract.RuleCatalogue, func(t *testing.T, s store.Store) {
			// The setup does not archive the catalogue for this case, so the
			// record names one that is not there.
			body, meta := good(t)
			put(t, s, okKey, body, meta)
		}},
		{"a catalogue key of the wrong depth", bucketcontract.RuleCatalogueKey, func(t *testing.T, s store.Store) {
			put(t, s, "catalogue/only-one-component", []byte("x"), nil)
		}},
		{"an empty catalogue", bucketcontract.RuleCatalogueBody, func(t *testing.T, s store.Store) {
			if err := s.Put(context.Background(), store.Object{
				Key: "catalogue/app/1.0.0", Body: []byte{}, RetainUntil: now.AddDate(1, 0, 0),
			}); err != nil {
				t.Skipf("the store takes no empty object: %v", err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			each(t, func(t *testing.T, s store.Store) {
				// The common catalogue is archived unless the case is about its
				// absence, so that only the rule under test is broken.
				if c.rule != bucketcontract.RuleCatalogue {
					archive := &writer.SchemaArchive{Store: s, RetainUntil: func(from time.Time) time.Time { return from.AddDate(10, 0, 0) }}
					if err := archive.EnsureCatalogue(context.Background(), common(t)); err != nil {
						t.Fatal(err)
					}
				}
				c.build(t, s)
				report, err := bucketcontract.Check(context.Background(), s, bucketcontract.Options{})
				if err != nil {
					t.Fatal(err)
				}
				if !report.Has(c.rule) {
					t.Fatalf("a bucket that breaks %s was not reported for it:\n%s", c.rule, report)
				}
			})
		})
	}
}

// A well-formed bucket by hand is clean, so that the refusals above are not
// the checker refusing everything.
func TestTheCheckerAcceptsAWellFormedBucketMadeByHand(t *testing.T) {
	each(t, func(t *testing.T, s store.Store) {
		archive := &writer.SchemaArchive{Store: s, RetainUntil: func(from time.Time) time.Time { return from.AddDate(10, 0, 0) }}
		if err := archive.EnsureCatalogue(context.Background(), common(t)); err != nil {
			t.Fatal(err)
		}
		hour := now.Truncate(time.Hour)
		body, meta := object(t, "security", "acme", 3)
		put(t, s, keyAt("security", "acme", hour, 1), body, meta)
		body, meta = object(t, "billing", "globex", 1)
		put(t, s, keyAt("billing", "globex", hour.Add(time.Hour), 2), body, meta)

		report, err := bucketcontract.Check(context.Background(), s, bucketcontract.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !report.OK() || report.Objects != 2 || report.Records != 4 {
			t.Fatalf("a conforming bucket was refused:\n%s", report)
		}
	})
}

// The profile is the first component and the checker honours a narrower scope:
// asked about one profile, it does not see another's faults.
func TestTheCheckerChecksOnlyTheProfilesAsked(t *testing.T) {
	each(t, func(t *testing.T, s store.Store) {
		archive := &writer.SchemaArchive{Store: s, RetainUntil: func(from time.Time) time.Time { return from.AddDate(10, 0, 0) }}
		if err := archive.EnsureCatalogue(context.Background(), common(t)); err != nil {
			t.Fatal(err)
		}
		hour := now.Truncate(time.Hour)
		body, meta := object(t, "security", "acme", 1)
		put(t, s, keyAt("security", "acme", hour, 1), body, meta)
		body, meta = object(t, "billing", "acme", 1)
		put(t, s, keyAt("billing", "acme", hour, 2), body, with(meta, "count", "9"))

		report, err := bucketcontract.Check(context.Background(), s, bucketcontract.Options{Profiles: []string{"security"}})
		if err != nil {
			t.Fatal(err)
		}
		if !report.OK() {
			t.Fatalf("billing's fault was reported against security:\n%s", report)
		}
		report, err = bucketcontract.Check(context.Background(), s, bucketcontract.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !report.Has(bucketcontract.RuleCount) {
			t.Fatalf("the whole bucket missed billing's fault:\n%s", report)
		}
	})
}
