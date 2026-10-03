package cli_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/internal/seal"
	"github.com/truvity/audit/internal/ulid"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/store"
	"github.com/truvity/audit/store/storetest"
)

// A tenant with objects in the first and third of three hours and none in the
// middle one: the quiet hour is the one a seal exists to make provable.
const (
	sealStart = "2026-09-17T10:00:00Z"
	settle    = 10 * time.Minute
)

type sealRig struct {
	t      *testing.T
	s      *storetest.Memory
	signer *keys.LocalSigner
	pub    *ecdsa.PublicKey
	start  time.Time
	// clock is the time the bucket stamps what is written with: the test's
	// own, so that it can be compared with a seal's sealed_at.
	clock time.Time
}

func newRig(t *testing.T) *sealRig {
	t.Helper()
	signer, err := keys.NewLocalP384("notary")
	if err != nil {
		t.Fatal(err)
	}
	public, _ := signer.PublicKey(context.Background())
	pub, err := keys.ParseECPublic(public)
	if err != nil {
		t.Fatal(err)
	}
	r := &sealRig{t: t, s: storetest.NewMemory(), signer: signer, pub: pub, start: at(t, sealStart)}
	r.clock = r.start.Add(3*time.Hour + 30*time.Minute)
	r.s.Now = func() time.Time { return r.clock }
	object(t, r.s, "security", "acme", r.start.Add(10*time.Minute),
		copyOf(t, "018f0000-0000-7000-8000-00000000000a", r.start),
		copyOf(t, "018f0000-0000-7000-8000-00000000000b", r.start))
	object(t, r.s, "security", "acme", r.start.Add(25*time.Minute),
		copyOf(t, "018f0000-0000-7000-8000-00000000000c", r.start))
	// hour 11 is quiet
	object(t, r.s, "security", "acme", r.start.Add(2*time.Hour+5*time.Minute),
		copyOf(t, "018f0000-0000-7000-8000-00000000000d", r.start))
	return r
}

func (r *sealRig) notary(signer keys.Signer, now time.Time) (cli.Notary, *strings.Builder) {
	out := &strings.Builder{}
	return cli.Notary{Store: r.s, Signer: signer, Settle: settle, Now: func() time.Time { return now }, Out: out}, out
}

// seal runs the notary as of `now`.
func (r *sealRig) seal(now time.Time) *cli.NotaryReport {
	r.t.Helper()
	r.clock = now
	n, _ := r.notary(r.signer, now)
	report, err := n.Run(context.Background())
	if err != nil {
		r.t.Fatalf("the notary: %v\n%+v", err, report)
	}
	return report
}

func (r *sealRig) verify(from, to, now time.Time, pins ...string) (int, string) {
	r.t.Helper()
	if len(pins) == 0 {
		pins = []string{seal.Thumbprint(r.pub)}
	}
	out := &strings.Builder{}
	n, err := cli.Verify{
		Store: r.s, Profile: "security", From: from, To: to, Out: out,
		Seals: &cli.SealCheck{Roots: pins, Settle: settle, Grace: time.Hour},
		Now:   func() time.Time { return now },
	}.Run(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	return n, out.String()
}

func sealOf(t *testing.T, s *storetest.Memory, hour time.Time) (*seal.Typed, []byte) {
	t.Helper()
	body, err := s.Get(context.Background(), store.SealKey("security", "acme", hour))
	if err != nil {
		t.Fatal(err)
	}
	typed, err := seal.ParseSeal(body)
	if err != nil {
		t.Fatal(err)
	}
	return typed, body
}

// The notary seals every closed hour since the last seal, the quiet ones too,
// and chains them: each names the hash of the one before.
func TestTheNotarySealsEveryClosedHourAndChainsThem(t *testing.T) {
	r := newRig(t)
	now := r.start.Add(3*time.Hour + 30*time.Minute)
	report := r.seal(now)
	if len(report.Sealed) != 3 || report.Present != 0 {
		t.Fatalf("sealed %d hours: %+v", len(report.Sealed), report)
	}
	first, firstBytes := sealOf(t, r.s, r.start)
	quiet, quietBytes := sealOf(t, r.s, r.start.Add(time.Hour))
	third, _ := sealOf(t, r.s, r.start.Add(2*time.Hour))

	if first.Seal.GetCount() != 3 || first.Seal.GetPrev() != "" || first.Seal.GetFirst() == first.Seal.GetLast() {
		t.Errorf("the first seal: %+v", first.Seal)
	}
	if first.Seal.GetMeters()["objects"] != 2 || first.Seal.GetMeters()["records"] != 3 || first.Seal.GetMeters()["bytes"] == 0 {
		t.Errorf("meters: %v", first.Seal.GetMeters())
	}
	empty := sha256.Sum256(nil)
	if quiet.Seal.GetCount() != 0 || quiet.Seal.GetRoot() != hex.EncodeToString(empty[:]) ||
		quiet.Seal.GetFirst() != "" || quiet.Seal.GetLast() != "" || len(quiet.Seal.GetMeters()) != 0 {
		t.Errorf("the quiet hour's seal: %+v", quiet.Seal)
	}
	if quiet.Seal.GetPrev() != seal.Hash(firstBytes) || third.Seal.GetPrev() != seal.Hash(quietBytes) {
		t.Error("the seals are not chained through prev")
	}
	if third.Seal.GetCount() != 1 {
		t.Errorf("the third seal: %+v", third.Seal)
	}
	for _, typed := range []*seal.Typed{first, quiet, third} {
		if typed.Token.Kid != report.Key || typed.Token.Typ != seal.TypSeal || typed.Token.Alg != "ES384" {
			t.Errorf("header %+v, key %s", typed.Token, report.Key)
		}
		if !typed.Seal.GetSealedAt().AsTime().Equal(now.Truncate(time.Second)) {
			t.Errorf("sealed_at %v", typed.Seal.GetSealedAt().AsTime())
		}
		if err := typed.Token.Verify(r.pub); err != nil {
			t.Errorf("a seal does not verify under the notary's key: %v", err)
		}
	}
	// A seal is locked as long as the records it covers.
	head, _ := r.s.Head(context.Background(), store.SealKey("security", "acme", r.start))
	if head.RetainUntil.IsZero() {
		t.Error("a seal carries no retention")
	}
	if got, err := r.s.Get(context.Background(), store.RootsKey); err != nil || !strings.Contains(string(got), seal.Thumbprint(r.pub)) {
		t.Errorf("keys/roots.jwks: %s, %v", got, err)
	}
}

// A second run over the same bucket writes nothing, and a later one seals only
// what has closed since.
func TestTheNotaryIsIdempotentAndResumes(t *testing.T) {
	r := newRig(t)
	now := r.start.Add(3*time.Hour + 30*time.Minute)
	r.seal(now)
	keysBefore := r.s.Keys()
	if report := r.seal(now); len(report.Sealed) != 0 {
		t.Fatalf("a rerun sealed %d hours", len(report.Sealed))
	}
	if after := r.s.Keys(); len(after) != len(keysBefore) {
		t.Fatalf("a rerun changed the bucket: %v", after)
	}

	// The next hour closes and settles: it is sealed, chained to the last.
	_, lastBytes := sealOf(t, r.s, r.start.Add(2*time.Hour))
	report := r.seal(r.start.Add(4*time.Hour + settle))
	if len(report.Sealed) != 1 || !report.Sealed[0].Hour.Equal(r.start.Add(3*time.Hour)) {
		t.Fatalf("resumed with %+v", report.Sealed)
	}
	next, _ := sealOf(t, r.s, r.start.Add(3*time.Hour))
	if next.Seal.GetPrev() != seal.Hash(lastBytes) || next.Seal.GetCount() != 0 {
		t.Errorf("the resumed seal: %+v", next.Seal)
	}
}

// An hour is sealed only after its end plus the settle window, so a batch put
// late in the hour it is keyed by is in the seal.
func TestTheNotaryWaitsForTheSettleWindow(t *testing.T) {
	r := newRig(t)
	if report := r.seal(r.start.Add(time.Hour + settle - time.Second)); len(report.Sealed) != 0 {
		t.Fatalf("an hour was sealed %s before it settled", time.Second)
	}
	if report := r.seal(r.start.Add(time.Hour + settle)); len(report.Sealed) != 1 {
		t.Fatalf("the hour was not sealed when it settled: %+v", report.Sealed)
	}
}

// A clean chain verifies, signed by the pinned root, and every seal is checked.
func TestVerifyAcceptsTheChain(t *testing.T) {
	r := newRig(t)
	now := r.start.Add(3*time.Hour + 30*time.Minute)
	r.seal(now)
	n, out := r.verify(r.start, r.start.Add(3*time.Hour), now)
	if n != 0 {
		t.Fatalf("%d problems on a clean chain:\n%s", n, out)
	}
	if !strings.Contains(out, "valid    seals/") {
		t.Fatalf("the report does not name the seals:\n%s", out)
	}
	// A range in the middle chains to the seal before it.
	if n, out := r.verify(r.start.Add(time.Hour), r.start.Add(3*time.Hour), now); n != 0 {
		t.Fatalf("a range inside the chain:\n%s", out)
	}
}

func recordKeys(r *sealRig) []string {
	var out []string
	for _, k := range r.s.Keys() {
		if strings.HasPrefix(k, store.RecordsPrefix) {
			out = append(out, k)
		}
	}
	return out
}

func problems(t *testing.T, r *sealRig, now time.Time, want ...string) {
	t.Helper()
	n, out := r.verify(r.start, r.start.Add(4*time.Hour), now)
	if n == 0 {
		t.Fatalf("a fault was not reported:\n%s", out)
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("the report does not say %q:\n%s", w, out)
		}
	}
}

// A removed seal is a missing seal for an hour that is due one, and the seal
// after it names a predecessor that is not there.
func TestVerifyReportsASealThatWasRemoved(t *testing.T) {
	r := newRig(t)
	now := r.start.Add(5 * time.Hour)
	r.seal(now)
	r.s.Forget(store.SealKey("security", "acme", r.start.Add(time.Hour)))
	problems(t, r, now, "there is no seal for the hour", "the seal for the previous hour is not in the bucket")
}

// A seal that was never written is the same fault, found without anything being
// removed: the notary stopped.
func TestVerifyReportsTheSealsANotaryNeverWrote(t *testing.T) {
	r := newRig(t)
	problems(t, r, r.start.Add(6*time.Hour), "there is no seal for the hour")
	// Not yet due is not a fault.
	if n, out := r.verify(r.start, r.start.Add(time.Hour), r.start.Add(time.Hour+settle+30*time.Minute)); n != 0 {
		t.Fatalf("a seal that is not due was reported:\n%s", out)
	}
}

// A batch put into a sealed hour changes what the hour holds.
func TestVerifyReportsAnObjectAddedToASealedHour(t *testing.T) {
	r := newRig(t)
	now := r.start.Add(5 * time.Hour)
	r.seal(now)
	object(t, r.s, "security", "acme", r.start.Add(40*time.Minute),
		copyOf(t, "018f0000-0000-7000-8000-0000000000aa", r.start))
	problems(t, r, now, "the hour no longer holds what was sealed", "it says 3 records and the hour holds 4")
}

// An object of a sealed hour that was removed.
func TestVerifyReportsAnObjectRemovedFromASealedHour(t *testing.T) {
	r := newRig(t)
	now := r.start.Add(5 * time.Hour)
	r.seal(now)
	r.s.Forget(recordKeys(r)[0])
	problems(t, r, now, "the hour no longer holds what was sealed")
}

// An object whose bytes were replaced and whose metadata was made to agree is
// not caught by the object's sha256: the root over the records' hashes is.
func TestVerifyReportsAChangedRecordThroughTheRoot(t *testing.T) {
	r := newRig(t)
	now := r.start.Add(5 * time.Hour)
	r.seal(now)
	key := store.HourPrefix("security", "acme", r.start)
	var victim string
	for _, k := range r.s.Keys() {
		if strings.HasPrefix(k, key) {
			victim = k
			break
		}
	}
	// A different record in the same place, with metadata to match.
	other := storetest.NewMemory()
	k := object(t, other, "security", "acme", r.start.Add(10*time.Minute),
		copyOf(t, "018f0000-0000-7000-8000-0000000000ff", r.start),
		copyOf(t, "018f0000-0000-7000-8000-0000000000fe", r.start))
	body, _ := other.Get(context.Background(), k)
	entry, _ := other.Head(context.Background(), k)
	r.s.Replace(victim, body)
	r.s.SetMetadata(victim, entry.Metadata)
	problems(t, r, now, "its root is")
}

// A key that no verifier pinned is not believed, however well it signs: writing
// to the bucket cannot add a root.
func TestVerifyDoesNotBelieveAKeyItDidNotPin(t *testing.T) {
	r := newRig(t)
	now := r.start.Add(3*time.Hour + 30*time.Minute)
	r.seal(now)
	n, out := r.verify(r.start, r.start.Add(3*time.Hour), now, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if n == 0 || !strings.Contains(out, "not a root this verifier pins") {
		t.Fatalf("an unpinned key was believed (%d problems):\n%s", n, out)
	}
}

// A seal whose prev is not the hash of the one before is a broken chain.
func TestVerifyReportsABrokenChain(t *testing.T) {
	r := newRig(t)
	now := r.start.Add(3*time.Hour + 30*time.Minute)
	r.seal(now)
	// Re-sign the third seal with another predecessor.
	third, _ := sealOf(t, r.s, r.start.Add(2*time.Hour))
	third.Seal.Prev = strings.Repeat("0", 64)
	payload, _ := seal.Marshal(third.Seal)
	forged, err := seal.Sign(context.Background(), r.signer, seal.TypSeal, payload)
	if err != nil {
		t.Fatal(err)
	}
	r.s.Replace(store.SealKey("security", "acme", r.start.Add(2*time.Hour)), forged)
	problems(t, r, now, "it chains to")
}

// The notary refuses to seal an hour whose objects do not match their own
// metadata, and seals nothing past it: the chain stops where it cannot vouch.
func TestTheNotaryRefusesAnHourItCannotVouchFor(t *testing.T) {
	r := newRig(t)
	for _, k := range r.s.Keys() {
		if strings.HasPrefix(k, store.HourPrefix("security", "acme", r.start.Add(time.Hour))) {
			t.Fatal("the quiet hour is not quiet")
		}
	}
	r.s.Replace(recordKeys(r)[0], []byte("not what was written"))
	r.clock = r.start.Add(4 * time.Hour)
	n, _ := r.notary(r.signer, r.clock)
	report, err := n.Run(context.Background())
	if err == nil || len(report.Failures) != 1 || len(report.Sealed) != 0 {
		t.Fatalf("a corrupt hour was sealed: %v %+v", err, report)
	}
	if !strings.Contains(report.Failures[0].Reason, "is not sealed") {
		t.Errorf("reason %q", report.Failures[0].Reason)
	}
}

// Another tenant's trouble does not stop a tenant that is fine.
func TestOneTenantsFaultDoesNotStopTheOthers(t *testing.T) {
	r := newRig(t)
	other := copyOf(t, "018f0000-0000-7000-8000-0000000000b1", r.start)
	other.TenantId = "globex"
	bad := object(t, r.s, "security", "globex", r.start.Add(5*time.Minute), other)
	r.s.Replace(bad, []byte("garbage"))
	n, _ := r.notary(r.signer, r.start.Add(3*time.Hour+30*time.Minute))
	report, err := n.Run(context.Background())
	if err == nil || len(report.Failures) != 1 || report.Failures[0].Tenant != "globex" {
		t.Fatalf("%v %+v", err, report)
	}
	if len(report.Sealed) != 3 {
		t.Fatalf("acme was sealed in %d hours", len(report.Sealed))
	}
}

// keys/roots.jwks is written once from the signer's key, and a notary with a
// key the file does not list refuses to sign.
func TestTheRootsFileIsWrittenOnceAndTheSignerMustBeInIt(t *testing.T) {
	r := newRig(t)
	r.seal(r.start.Add(3*time.Hour + 30*time.Minute))
	first, _ := r.s.Get(context.Background(), store.RootsKey)

	other, _ := keys.NewLocalP384("other")
	n, _ := r.notary(other, r.start.Add(5*time.Hour))
	_, err := n.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "does not list the signing key") {
		t.Fatalf("a notary signed with a key the roots file does not list: %v", err)
	}
	if again, _ := r.s.Get(context.Background(), store.RootsKey); string(again) != string(first) {
		t.Error("the roots file was written twice")
	}
	// An ed25519 key is not a seal key.
	ed, _ := keys.NewLocalSigner("ed")
	n, _ = r.notary(ed, r.start.Add(5*time.Hour))
	if _, err := n.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "P-384") {
		t.Fatalf("an ed25519 key was accepted: %v", err)
	}
}

// delegate signs a delegation to `sub` with the root and puts it in the bucket.
func (r *sealRig) delegate(sub *keys.LocalSigner, nbf, exp time.Time, profiles, tenants []string) {
	r.t.Helper()
	public, _ := sub.PublicKey(context.Background())
	pub, _ := keys.ParseECPublic(public)
	d, err := seal.NewDelegation(seal.Thumbprint(r.pub), seal.NewJWK(pub), nbf, exp, profiles, tenants)
	if err != nil {
		r.t.Fatal(err)
	}
	payload, err := seal.Marshal(d)
	if err != nil {
		r.t.Fatal(err)
	}
	compact, err := seal.Sign(context.Background(), r.signer, seal.TypDelegation, payload)
	if err != nil {
		r.t.Fatal(err)
	}
	key := store.DelegationsPrefix + seal.Thumbprint(pub) + "/" + ulid.From(nbf, 1) + ".jws"
	if err := r.s.Put(context.Background(), store.Object{Key: key, Body: compact}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *sealRig) revoke(who *ecdsa.PublicKey, at time.Time) {
	r.t.Helper()
	payload, err := seal.Marshal(&seal.Revocation{
		Iss: seal.Thumbprint(r.pub), Revokes: seal.Thumbprint(who), RevokedAt: timestamppbOf(at), Reason: "test",
	})
	if err != nil {
		r.t.Fatal(err)
	}
	compact, err := seal.Sign(context.Background(), r.signer, seal.TypRevocation, payload)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := r.s.Put(context.Background(), store.Object{
		Key: store.RevocationsPrefix + ulid.From(at, 2) + ".jws", Body: compact,
	}); err != nil {
		r.t.Fatal(err)
	}
}

// A seal signed by a key a pinned root delegated to is believed inside the
// window and the scope, and not outside them.
func TestVerifyBelievesADelegateInItsWindowAndScope(t *testing.T) {
	now := at(t, sealStart).Add(3*time.Hour + 30*time.Minute)
	for name, c := range map[string]struct {
		nbf, exp         time.Time
		profiles, tenant []string
		wantProblem      string
	}{
		"in the window and the scope": {now.Add(-time.Hour), now.Add(23 * time.Hour), []string{"security"}, []string{"*"}, ""},
		"before the window":           {now.Add(time.Hour), now.Add(2 * time.Hour), []string{"*"}, []string{"*"}, "outside the window"},
		"after the window":            {now.Add(-3 * time.Hour), now.Add(-2 * time.Hour), []string{"*"}, []string{"*"}, "outside the window"},
		"another profile":             {now.Add(-time.Hour), now.Add(time.Hour), []string{"billing"}, []string{"*"}, "outside the delegation's scope"},
		"another tenant":              {now.Add(-time.Hour), now.Add(time.Hour), []string{"*"}, []string{"globex"}, "outside the delegation's scope"},
		"more than 25 hours":          {now.Add(-time.Hour), now.Add(25 * time.Hour), []string{"*"}, []string{"*"}, "no delegation"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			delegate, _ := keys.NewLocalP384("delegate")
			r.delegate(delegate, c.nbf, c.exp, c.profiles, c.tenant)
			// The root puts keys/roots.jwks; the delegate seals.
			r.seal(now)
			// A delegate is not in the roots file, and the notary refuses to
			// sign with a key the file does not list (the delegation lists it,
			// and signer-side delegation is not built): seal as the root, then
			// re-sign every seal as the delegate.
			resign(t, r, delegate, now)
			code, out := r.verify(r.start, r.start.Add(3*time.Hour), now)
			switch {
			case c.wantProblem == "" && code != 0:
				t.Fatalf("a delegate inside its window and scope was refused:\n%s", out)
			case c.wantProblem != "" && (code == 0 || !strings.Contains(out, c.wantProblem)):
				t.Fatalf("want %q (%d problems):\n%s", c.wantProblem, code, out)
			}
		})
	}
}

// resign replaces every seal in the bucket with the same statement signed by
// another key, keeping the chain by re-hashing as it goes.
func resign(t *testing.T, r *sealRig, by keys.Signer, at time.Time) {
	t.Helper()
	prev := ""
	for h := r.start; h.Before(r.start.Add(3 * time.Hour)); h = h.Add(time.Hour) {
		typed, _ := sealOf(t, r.s, h)
		typed.Seal.Prev = prev
		typed.Seal.SealedAt = timestamppbOf(at)
		payload, _ := seal.Marshal(typed.Seal)
		compact, err := seal.Sign(context.Background(), by, seal.TypSeal, payload)
		if err != nil {
			t.Fatal(err)
		}
		r.s.Replace(store.SealKey("security", "acme", h), compact)
		prev = seal.Hash(compact)
	}
}

// A revocation ends the trust in a delegate from its time; a seal signed
// before it stands.
func TestVerifyDoesNotBelieveARevokedDelegate(t *testing.T) {
	now := at(t, sealStart).Add(3*time.Hour + 30*time.Minute)
	for name, c := range map[string]struct {
		revokedAt time.Time
		want      string
	}{
		"revoked before the seal was made": {now.Add(-time.Minute), "was revoked from"},
		"revoked after the seal was made":  {now.Add(time.Hour), ""},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			delegate, _ := keys.NewLocalP384("delegate")
			r.delegate(delegate, now.Add(-time.Hour), now.Add(time.Hour), []string{"*"}, []string{"*"})
			r.seal(now)
			resign(t, r, delegate, now)
			public, _ := delegate.PublicKey(context.Background())
			pub, _ := keys.ParseECPublic(public)
			r.revoke(pub, c.revokedAt)
			code, out := r.verify(r.start, r.start.Add(3*time.Hour), now)
			switch {
			case c.want == "" && code != 0:
				t.Fatalf("a seal made before the revocation was refused:\n%s", out)
			case c.want != "" && (code == 0 || !strings.Contains(out, c.want)):
				t.Fatalf("a revoked key was believed (%d problems):\n%s", code, out)
			}
		})
	}
}

// A delegation or a revocation signed by a key that is not a pinned root is
// ignored: it is only in the bucket.
func TestVerifyIgnoresStatementsOfARootItDidNotPin(t *testing.T) {
	now := at(t, sealStart).Add(3*time.Hour + 30*time.Minute)
	r := newRig(t)
	rogue, _ := keys.NewLocalP384("rogue")
	delegate, _ := keys.NewLocalP384("delegate")
	// The rogue root delegates to the delegate; the verifier pins the real root.
	realSigner := r.signer
	r.signer = rogue
	r.pub, _ = keys.ParseECPublic(mustPublic(t, rogue))
	r.delegate(delegate, now.Add(-time.Hour), now.Add(time.Hour), []string{"*"}, []string{"*"})
	r.signer = realSigner
	r.pub, _ = keys.ParseECPublic(mustPublic(t, realSigner))

	r.seal(now)
	resign(t, r, delegate, now)
	code, out := r.verify(r.start, r.start.Add(3*time.Hour), now)
	if code == 0 {
		t.Fatalf("a delegation from a root nobody pinned was believed:\n%s", out)
	}
}

func mustPublic(t *testing.T, s keys.Signer) []byte {
	t.Helper()
	p, err := s.PublicKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The notary records each seal it writes, and verify records each hour it
// checked — only when it checked seals.
func TestSealEventsAreRecordedWhereTheyAreMeant(t *testing.T) {
	r := newRig(t)
	into := &collector{}
	now := r.start.Add(3*time.Hour + 30*time.Minute)
	n, _ := r.notary(r.signer, now)
	n.Sink, n.Catalogue, n.Instance = into, common(t), "notary-1"
	if _, err := n.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	written := events(into, "audit.seal.written")
	if len(written) != 3 {
		t.Fatalf("%d seal.written events for 3 seals", len(written))
	}
	if got := written[0].GetTargets()[0]; got.GetType() != "seal" || !strings.HasPrefix(got.GetId(), "seals/security/acme/2026/09/17/10") {
		t.Errorf("target %v", got)
	}

	verified := &collector{}
	out := &strings.Builder{}
	code, err := cli.Verify{
		Store: r.s, Profile: "security", From: r.start, To: r.start.Add(3 * time.Hour), Out: out,
		Seals: &cli.SealCheck{Roots: []string{seal.Thumbprint(r.pub)}, Settle: settle, Grace: time.Hour},
		Now:   func() time.Time { return now }, Sink: verified, Catalogue: common(t), Instance: "verify-1",
	}.Run(context.Background())
	if err != nil || code != 0 {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := len(events(verified, "audit.seal.verified")); got != 3 {
		t.Fatalf("%d seal.verified events for 3 hours (quiet hour included)", got)
	}

	// Without seals there is nothing to say about any: no event.
	none := &collector{}
	if _, err := (cli.Verify{
		Store: r.s, Profile: "security", From: r.start, To: r.start.Add(3 * time.Hour), Out: &strings.Builder{},
		Sink: none, Catalogue: common(t), Instance: "verify-1",
	}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(none.records) != 0 {
		t.Fatalf("a run that checked no seal recorded %d events", len(none.records))
	}
}

func timestamppbOf(t time.Time) *timestamppb.Timestamp { return timestamppb.New(t) }
