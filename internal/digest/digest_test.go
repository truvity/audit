package digest_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/truvity/audit/internal/digest"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/store"
	"github.com/truvity/audit/store/storetest"
)

type built struct {
	store    *storetest.Memory
	builder  *digest.Builder
	verifier *digest.Verifier
	signer   *keys.LocalSigner
}

func setup(t *testing.T) *built {
	t.Helper()
	signer, err := keys.NewLocalSigner("test")
	if err != nil {
		t.Fatal(err)
	}
	pub, err := signer.PublicKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s := storetest.NewMemory()
	return &built{
		store:    s,
		builder:  &digest.Builder{Store: s, Signer: signer},
		verifier: &digest.Verifier{Store: s, PublicKeyPEM: pub},
		signer:   signer,
	}
}

func at(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.UTC()
}

// put writes an object as the writer would, under the profile-first layout,
// appearing at a given moment so that a window can be about write time.
func put(t *testing.T, b *built, day time.Time, name, body string) string {
	t.Helper()
	key := "profile=security/tenant=acme/year=" + day.Format("2006") +
		"/month=" + day.Format("01") + "/day=" + day.Format("02") + "/" + name
	b.store.Now = func() time.Time { return day.Add(10*time.Hour + 30*time.Minute) }
	if err := b.store.Put(context.Background(), store.Object{
		Key: key, Body: []byte(body), RetainUntil: day.AddDate(1, 0, 0),
	}); err != nil {
		t.Fatal(err)
	}
	return key
}

// seal builds and writes the digest of one window.
func seal(t *testing.T, b *built, start time.Time) string {
	t.Helper()
	d, err := b.builder.Build(context.Background(), "security", "profile=security",
		start, start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	key, err := b.builder.Write(context.Background(), d, start.AddDate(1, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestAChainOfCleanWindowsVerifies(t *testing.T) {
	b := setup(t)
	day := at(t, "2026-09-17T00:00:00Z")
	put(t, b, day, "a.ndjson.zst", "one")
	put(t, b, day, "b.ndjson.zst", "two")

	window := at(t, "2026-09-17T10:00:00Z")
	seal(t, b, window)
	seal(t, b, window.Add(time.Hour))

	report, err := b.verifier.Verify(context.Background(), "security",
		window, window.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK() {
		t.Fatalf("a clean chain did not verify:\n%s", report)
	}
	if report.Digests != 2 {
		t.Fatalf("checked %d digests, want 2", report.Digests)
	}
	if report.Objects == 0 {
		t.Fatal("no objects were checked")
	}
}

// An hour with no digest cannot be told from an hour whose digest was removed,
// so a digest is written for a quiet window too.
func TestAQuietWindowStillGetsADigest(t *testing.T) {
	b := setup(t)
	window := at(t, "2026-09-17T10:00:00Z")
	key := seal(t, b, window)

	d, err := digest.Read(context.Background(), b.store, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Objects) != 0 {
		t.Fatalf("the window covers %d objects, want none", len(d.Objects))
	}
	if d.Signature == "" {
		t.Fatal("an empty window's digest is unsigned, so its absence would prove nothing")
	}
	report, err := b.verifier.Verify(context.Background(), "security", window, window.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK() {
		t.Fatalf("a quiet window did not verify:\n%s", report)
	}
}

// The three things an auditor asks: was an object changed, was one taken away,
// was one slipped in.
func TestVerifyCatchesTampering(t *testing.T) {
	window := at(t, "2026-09-17T10:00:00Z")
	day := at(t, "2026-09-17T00:00:00Z")

	t.Run("an object was changed", func(t *testing.T) {
		b := setup(t)
		key := put(t, b, day, "a.ndjson.zst", "one")
		seal(t, b, window)

		// A real bucket would refuse this; the point of the chain is that a
		// store which did not would still be caught.
		b.store.Replace(key, []byte("tampered"))

		report, err := b.verifier.Verify(context.Background(), "security", window, window.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if report.OK() {
			t.Fatal("a changed object was not caught")
		}
		if !strings.Contains(report.String(), "changed since it was signed") {
			t.Fatalf("report does not say what is wrong:\n%s", report)
		}
	})

	t.Run("an object was taken away", func(t *testing.T) {
		b := setup(t)
		key := put(t, b, day, "a.ndjson.zst", "one")
		seal(t, b, window)
		b.store.Forget(key)

		report, err := b.verifier.Verify(context.Background(), "security", window, window.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if report.OK() {
			t.Fatal("a missing object was not caught")
		}
		if !strings.Contains(report.String(), "missing") {
			t.Fatalf("report does not say what is wrong:\n%s", report)
		}
	})

	t.Run("an object was slipped in", func(t *testing.T) {
		b := setup(t)
		put(t, b, day, "a.ndjson.zst", "one")
		seal(t, b, window)
		// Written after the window was sealed, but backdated to sit inside it.
		key := put(t, b, day, "smuggled.ndjson.zst", "not accounted for")
		b.store.Backdate(key, window.Add(30*time.Minute))

		report, err := b.verifier.Verify(context.Background(), "security", window, window.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if report.OK() {
			t.Fatal("an object no digest accounts for was not caught")
		}
		if !strings.Contains(report.String(), "no digest accounts") {
			t.Fatalf("report does not say what is wrong:\n%s", report)
		}
	})

	t.Run("a digest in the middle was removed", func(t *testing.T) {
		b := setup(t)
		put(t, b, day, "a.ndjson.zst", "one")
		seal(t, b, window)
		middle := seal(t, b, window.Add(time.Hour))
		seal(t, b, window.Add(2*time.Hour))
		b.store.Forget(middle)

		report, err := b.verifier.Verify(context.Background(), "security", window, window.Add(4*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if report.OK() {
			t.Fatal("a removed digest broke no link")
		}
		if !strings.Contains(report.String(), "is missing") {
			t.Fatalf("report does not say what is wrong:\n%s", report)
		}
	})

	t.Run("a digest was rewritten", func(t *testing.T) {
		b := setup(t)
		put(t, b, day, "a.ndjson.zst", "one")
		first := seal(t, b, window)
		seal(t, b, window.Add(time.Hour))

		body, err := b.store.Get(context.Background(), first)
		if err != nil {
			t.Fatal(err)
		}
		b.store.Replace(first, []byte(strings.Replace(string(body), "security", "securitY", 1)))

		report, err := b.verifier.Verify(context.Background(), "security", window, window.Add(3*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if report.OK() {
			t.Fatal("a rewritten digest was not caught")
		}
	})
}

// A signature from another key is no signature at all.
func TestVerifyRefusesAnotherKeysSignature(t *testing.T) {
	b := setup(t)
	window := at(t, "2026-09-17T10:00:00Z")
	seal(t, b, window)

	other, err := keys.NewLocalSigner("other")
	if err != nil {
		t.Fatal(err)
	}
	pub, err := other.PublicKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b.verifier.PublicKeyPEM = pub

	report, err := b.verifier.Verify(context.Background(), "security", window, window.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if report.OK() {
		t.Fatal("a digest signed by another key must not verify")
	}
}

// A range with no digest at all is itself the finding.
func TestVerifyReportsAnEmptyRange(t *testing.T) {
	b := setup(t)
	window := at(t, "2026-09-17T10:00:00Z")
	report, err := b.verifier.Verify(context.Background(), "security", window, window.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if report.OK() {
		t.Fatal("a range with no digest must be reported, not passed")
	}
}

// An object whose lock ends sooner than its profile requires is a fault in the
// writer or the bucket, and the verifier is where it surfaces.
func TestVerifyChecksRetention(t *testing.T) {
	b := setup(t)
	b.verifier.MinimumRetention = map[string]time.Duration{"security": 365 * 24 * time.Hour}
	day := at(t, "2026-09-17T00:00:00Z")
	window := at(t, "2026-09-17T10:00:00Z")

	key := "profile=security/tenant=acme/year=2026/month=09/day=17/short.ndjson.zst"
	b.store.Now = func() time.Time { return window.Add(30 * time.Minute) }
	if err := b.store.Put(context.Background(), store.Object{
		Key: key, Body: []byte("one"), RetainUntil: day.AddDate(0, 1, 0),
	}); err != nil {
		t.Fatal(err)
	}
	seal(t, b, window)

	report, err := b.verifier.Verify(context.Background(), "security", window, window.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if report.OK() {
		t.Fatal("an object locked for a month under a year-long profile was not caught")
	}
	if !strings.Contains(report.String(), "sooner than profile") {
		t.Fatalf("report does not say what is wrong:\n%s", report)
	}
}

func TestBuilderChecksItsParts(t *testing.T) {
	if err := (&digest.Builder{}).Check(); err == nil {
		t.Error("want a refusal with no store")
	}
	if err := (&digest.Builder{Store: storetest.NewMemory()}).Check(); err == nil {
		t.Error("want a refusal with no signer")
	}
}

// putFor writes an object under a named tenant.
func putFor(t *testing.T, b *built, tenant string, day time.Time, name, body string) string {
	t.Helper()
	key := "profile=security/tenant=" + tenant + "/year=" + day.Format("2006") +
		"/month=" + day.Format("01") + "/day=" + day.Format("02") + "/" + name
	b.store.Now = func() time.Time { return day.Add(10*time.Hour + 30*time.Minute) }
	if err := b.store.Put(context.Background(), store.Object{
		Key: key, Body: []byte(body), RetainUntil: day.AddDate(1, 0, 0),
	}); err != nil {
		t.Fatal(err)
	}
	return key
}

// A digest is per profile, and a profile holds every tenant's copies. The
// tenant sits between the profile and the date in the key, so a builder that
// treats the profile prefix as if the date came next covers one tenant and
// silently leaves the rest to be reported as unaccounted for.
func TestADigestCoversEveryTenantOfItsProfile(t *testing.T) {
	b := setup(t)
	day := at(t, "2026-09-17T00:00:00Z")
	window := at(t, "2026-09-17T10:00:00Z")

	acme := putFor(t, b, "acme", day, "a.ndjson.zst", "one")
	globex := putFor(t, b, "globex", day, "b.ndjson.zst", "two")

	d, err := b.builder.Build(context.Background(), "security", "profile=security", window, window.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	covered := map[string]bool{}
	for _, o := range d.Objects {
		covered[o.Key] = true
	}
	if !covered[acme] || !covered[globex] {
		t.Fatalf("the digest covers %d of 2 tenants: %v", len(d.Objects), covered)
	}

	if _, err := b.builder.Write(context.Background(), d, day.AddDate(1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	report, err := b.verifier.Verify(context.Background(), "security", window, window.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if problems := report.Problems(); len(problems) != 0 {
		t.Fatalf("a chain covering both tenants still reports %d problems: %v", len(problems), problems)
	}
}
