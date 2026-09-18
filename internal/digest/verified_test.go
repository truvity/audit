package digest_test

import (
	"context"
	"testing"
	"time"

	"github.com/truvity/audit/internal/digest"
)

// A record's standing in the chain, as Get reports it: the digest that names
// its object, and when that was last verified clean — and nothing claimed that
// is not so.
func TestProvenanceNamesTheDigestAndItsLastCleanVerification(t *testing.T) {
	b := setup(t)
	ctx := context.Background()
	day := at(t, "2026-09-17T00:00:00Z")
	key := put(t, b, day, "a.ndjson.zst", "one") // written at 10:30
	window := at(t, "2026-09-17T10:00:00Z")

	// Before its hour is sealed an object has no digest: the ordinary state of
	// the current hour.
	if p, err := digest.ProvenanceOf(ctx, b.store, "security", key); err != nil || p.Digest != "" {
		t.Fatalf("before sealing: %+v %v", p, err)
	}

	digestKey := seal(t, b, window)
	p, err := digest.ProvenanceOf(ctx, b.store, "security", key)
	if err != nil {
		t.Fatal(err)
	}
	if p.Digest != digestKey || !p.VerifiedAt.IsZero() {
		t.Fatalf("sealed, never verified: %+v", p)
	}

	checked := at(t, "2026-09-18T03:23:00Z")
	if err := digest.RecordVerification(ctx, b.store, digest.Verification{
		Profile: "security", Digest: digestKey, WindowStart: window, VerifiedAt: checked, OK: true,
	}, window.AddDate(1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if p, _ := digest.ProvenanceOf(ctx, b.store, "security", key); !p.VerifiedAt.Equal(checked) {
		t.Fatalf("verified clean: %+v", p)
	}

	// A later verification that found a problem: the answer to "has this been
	// checked" must not stay yes.
	if err := digest.RecordVerification(ctx, b.store, digest.Verification{
		Profile: "security", Digest: digestKey, WindowStart: window,
		VerifiedAt: checked.Add(24 * time.Hour), OK: false, Problems: 1,
	}, window.AddDate(1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if p, _ := digest.ProvenanceOf(ctx, b.store, "security", key); p.Digest != digestKey || !p.VerifiedAt.IsZero() {
		t.Fatalf("after a failed verification: %+v", p)
	}

	// A window whose digest exists but does not name the object is not an
	// account of it.
	other := put(t, b, day, "b.ndjson.zst", "two") // also 10:30, after sealing
	if p, _ := digest.ProvenanceOf(ctx, b.store, "security", other); p.Digest != "" {
		t.Fatalf("an object the digest does not name: %+v", p)
	}
}
