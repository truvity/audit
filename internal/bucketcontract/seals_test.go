package bucketcontract_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/truvity/audit/internal/bucketcontract"
	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/internal/s3test"
	"github.com/truvity/audit/internal/seal"
	"github.com/truvity/audit/internal/ulid"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/store"
)

// The seals half of the conformance suite
// (docs/reference/bucket-contract.md, "Seals"). Like the records half it runs
// against the in-memory store and against S3, and the notary runs with two
// signers: a key on this machine, and an ECC_NIST_P384 key in KMS (LocalStack's)
// where there is one.

const (
	settleWindow = 10 * time.Minute
	grace        = time.Hour
)

// The writer's clock is the suite's `now`, 10:40 on the 17th, and the batches
// are in hours 10, 11 and 12. This is when the notary runs.
var sealNow = time.Date(2026, 9, 17, 15, 0, 0, 0, time.UTC)

type signerCase struct {
	name string
	open func(*testing.T) keys.Signer
}

var signers = []signerCase{
	{"local", func(t *testing.T) keys.Signer {
		s, err := keys.NewLocalP384("conformance")
		if err != nil {
			t.Fatal(err)
		}
		return s
	}},
	{"kms", func(t *testing.T) keys.Signer {
		c := s3test.KMS(t)
		return &keys.KMSSigner{Client: c, Key: s3test.SigningKey(t, c, types.KeySpecEccNistP384)}
	}},
}

func pinOf(t *testing.T, s keys.Signer) string {
	t.Helper()
	public, err := s.PublicKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pub, err := keys.ParseECPublic(public)
	if err != nil {
		t.Fatal(err)
	}
	return seal.Thumbprint(pub)
}

// notarise runs the notary over the bucket as of `at`.
func notarise(t *testing.T, s store.Store, signer keys.Signer, at time.Time) *cli.NotaryReport {
	t.Helper()
	report, err := cli.Notary{
		Store: s, Signer: signer, Settle: settleWindow, Now: func() time.Time { return at }, Out: &strings.Builder{},
	}.Run(context.Background())
	if err != nil {
		t.Fatalf("the notary: %v\n%+v", err, report)
	}
	return report
}

func checkSeals(t *testing.T, s store.Store, profile string, at time.Time, last time.Time, pins ...string) *bucketcontract.SealReport {
	t.Helper()
	report, err := bucketcontract.CheckSeals(context.Background(), s, bucketcontract.SealOptions{
		Profile: profile, Roots: pins,
		From: now.Truncate(time.Hour), To: last,
		Now: at, Settle: settleWindow, Grace: grace,
	})
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func eachSigner(t *testing.T, run func(t *testing.T, s store.Store, signer keys.Signer)) {
	t.Helper()
	for _, sc := range signers {
		t.Run(sc.name, func(t *testing.T) {
			each(t, func(t *testing.T, s store.Store) { run(t, s, sc.open(t)) })
		})
	}
}

// What the notary writes conforms, on every store and with every signer.
func TestWhatTheNotarySealsConforms(t *testing.T) {
	eachSigner(t, func(t *testing.T, s store.Store, signer keys.Signer) {
		written(t, s, nil)
		report := notarise(t, s, signer, sealNow)
		// Hours 10 to 13 closed and settled, for each of the three
		// (profile, tenant) pairs; hour 13 is a quiet one for all of them.
		if len(report.Sealed) != 12 {
			t.Fatalf("sealed %d hours, want 12:\n%+v", len(report.Sealed), report.Sealed)
		}
		for _, profile := range []string{"security", "billing"} {
			res := checkSeals(t, s, profile, sealNow, now.Truncate(time.Hour).Add(4*time.Hour), pinOf(t, signer))
			if len(res.Findings) != 0 {
				t.Fatalf("the notary's own seals do not conform:\n%s", res)
			}
		}
		// A second run writes nothing: every hour is sealed.
		if again := notarise(t, s, signer, sealNow); len(again.Sealed) != 0 {
			t.Fatalf("a second run sealed %d hours", len(again.Sealed))
		}
		// The key's file is there once, and a seal is put once.
		if body, err := s.Get(context.Background(), store.RootsKey); err != nil || !strings.Contains(string(body), pinOf(t, signer)) {
			t.Fatalf("keys/roots.jwks: %v", err)
		}
		err := s.Put(context.Background(), store.Object{
			Key: store.SealKey("security", "acme", now.Truncate(time.Hour)), Body: []byte("x"), RetainUntil: now.AddDate(1, 0, 0),
		})
		if err == nil {
			t.Fatal("a second put to a seal's key was accepted")
		}
	})
}

// The seals checked a second way: by hand, with the standard library and a zstd
// reader, and nothing of this repository's seal, merkle or recobj packages. A
// verifier in another language has only the contract, so the contract has to be
// enough.
func TestTheSealsByHand(t *testing.T) {
	eachSigner(t, func(t *testing.T, s store.Store, signer keys.Signer) {
		ctx := context.Background()
		written(t, s, nil)
		notarise(t, s, signer, sealNow)

		// keys/roots.jwks: one P-384 key.
		jwksBody, err := s.Get(ctx, store.RootsKey)
		if err != nil {
			t.Fatal(err)
		}
		var jwks struct {
			Keys []struct{ Kty, Crv, X, Y, Kid string }
		}
		if err := json.Unmarshal(jwksBody, &jwks); err != nil || len(jwks.Keys) != 1 || jwks.Keys[0].Kty != "EC" || jwks.Keys[0].Crv != "P-384" {
			t.Fatalf("roots.jwks: %v %s", err, jwksBody)
		}
		k := jwks.Keys[0]
		dec := func(v string) []byte {
			b, err := base64.RawURLEncoding.DecodeString(v)
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
		pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P384(), append(append([]byte{4}, dec(k.X)...), dec(k.Y)...))
		if err != nil {
			t.Fatal(err)
		}
		// RFC 7638: SHA-256 of the required members, in order, no whitespace.
		thumb := sha256.Sum256([]byte(`{"crv":"P-384","kty":"EC","x":"` + k.X + `","y":"` + k.Y + `"}`))
		if base64.RawURLEncoding.EncodeToString(thumb[:]) != k.Kid {
			t.Fatalf("the kid %s is not the RFC 7638 thumbprint", k.Kid)
		}

		hour := now.Truncate(time.Hour)
		body, err := s.Get(ctx, "seals/security/acme/2026/09/17/10.jws")
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(string(body), ".")
		if len(parts) != 3 {
			t.Fatalf("not a compact JWS: %d parts", len(parts))
		}
		var header struct{ Alg, Typ, Kid string }
		if err := json.Unmarshal(dec(parts[0]), &header); err != nil || header.Alg != "ES384" || header.Typ != "audit-seal+jws" || header.Kid != k.Kid {
			t.Fatalf("header %+v (%v)", header, err)
		}
		sig := dec(parts[2])
		digest := sha512.Sum384([]byte(parts[0] + "." + parts[1]))
		if len(sig) != 96 || !ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:48]), new(big.Int).SetBytes(sig[48:])) {
			t.Fatal("the signature is not ES384 over the signing input")
		}

		var payload struct {
			Tenant, Profile, Hour, Root, First, Last, Prev string
			Count                                          string
			SealedAt                                       string `json:"sealed_at"`
			Meters                                         map[string]string
		}
		if err := json.Unmarshal(dec(parts[1]), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Tenant != "acme" || payload.Profile != "security" || payload.Hour != hour.Format(time.RFC3339) || payload.Prev != "" {
			t.Fatalf("payload %+v", payload)
		}

		// The root: RFC 6962 over the raw bytes of each record's hash, by object
		// key and then line, written out recursively.
		listed, err := s.List(ctx, store.HourPrefix("security", "acme", hour), "", 0)
		if err != nil {
			t.Fatal(err)
		}
		zr, _ := zstd.NewReader(nil)
		defer zr.Close()
		var leaves [][]byte
		for _, e := range listed {
			stored, err := s.Get(ctx, e.Key)
			if err != nil {
				t.Fatal(err)
			}
			plain, err := zr.DecodeAll(stored, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(strings.TrimSuffix(string(plain), "\n"), "\n") {
				var l struct{ Hash string }
				if err := json.Unmarshal([]byte(line), &l); err != nil {
					t.Fatal(err)
				}
				raw, err := hex.DecodeString(l.Hash)
				if err != nil || len(raw) != 32 {
					t.Fatalf("hash %q", l.Hash)
				}
				leaves = append(leaves, raw)
			}
		}
		if payload.Count != "4" || len(leaves) != 4 {
			t.Fatalf("count %s, %d records", payload.Count, len(leaves))
		}
		var mth func([][]byte) []byte
		mth = func(d [][]byte) []byte {
			switch len(d) {
			case 0:
				e := sha256.Sum256(nil)
				return e[:]
			case 1:
				h := sha256.Sum256(append([]byte{0}, d[0]...))
				return h[:]
			}
			split := 1
			for split*2 < len(d) {
				split *= 2
			}
			l, r := mth(d[:split]), mth(d[split:])
			h := sha256.Sum256(append(append([]byte{1}, l...), r...))
			return h[:]
		}
		if hex.EncodeToString(mth(leaves)) != payload.Root {
			t.Fatalf("the root is %s and RFC 6962 over the hashes gives %x", payload.Root, mth(leaves))
		}
		if payload.First != listed[0].Key || payload.Last != listed[len(listed)-1].Key {
			t.Fatalf("first and last: %s %s", payload.First, payload.Last)
		}

		// The next seal chains to the bytes of this one.
		next, err := s.Get(ctx, "seals/security/acme/2026/09/17/11.jws")
		if err != nil {
			t.Fatal(err)
		}
		var nextPayload struct{ Prev string }
		_ = json.Unmarshal(dec(strings.Split(string(next), ".")[1]), &nextPayload)
		sum := sha256.Sum256(body)
		if nextPayload.Prev != hex.EncodeToString(sum[:]) {
			t.Fatalf("prev is %s, the hash of the previous seal is %x", nextPayload.Prev, sum)
		}

		// The quiet hour: count zero and the SHA-256 of the empty string.
		quiet, err := s.Get(ctx, "seals/security/acme/2026/09/17/13.jws")
		if err != nil {
			t.Fatal(err)
		}
		var q struct{ Count, Root, First, Last string }
		_ = json.Unmarshal(dec(strings.Split(string(quiet), ".")[1]), &q)
		if q.Count != "0" || q.Root != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" || q.First != "" || q.Last != "" {
			t.Fatalf("the quiet hour: %+v", q)
		}
	})
}

// forge puts a seal for an hour the notary has not sealed, signed by `by`,
// chained to the seal before it, after `change` has had its way with the
// statement. It is how the suite shows each rule is held by something.
func forge(
	t *testing.T, s store.Store, by keys.Signer, tenant string, hour time.Time, at time.Time, change func(*seal.Seal),
) string {
	t.Helper()
	ctx := context.Background()
	computed, err := seal.Compute(ctx, s, "security", tenant, hour, false)
	if err != nil {
		t.Fatal(err)
	}
	prev := ""
	if before, err := s.Get(ctx, store.SealKey("security", tenant, hour.Add(-time.Hour))); err == nil {
		prev = seal.Hash(before)
	}
	statement := seal.NewSeal("security", tenant, hour, computed, prev, at)
	if change != nil {
		change(statement)
	}
	payload, err := seal.Marshal(statement)
	if err != nil {
		t.Fatal(err)
	}
	compact, err := seal.Sign(ctx, by, seal.TypSeal, payload)
	if err != nil {
		t.Fatal(err)
	}
	key := store.SealKey("security", tenant, hour)
	if err := s.Put(ctx, store.Object{Key: key, Body: compact, RetainUntil: now.AddDate(2, 0, 0)}); err != nil {
		t.Fatal(err)
	}
	return key
}

// putStatement signs a delegation or a revocation with the root and puts it.
func putStatement(t *testing.T, s store.Store, root keys.Signer, typ, key string, payload []byte) {
	t.Helper()
	compact, err := seal.Sign(context.Background(), root, typ, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(context.Background(), store.Object{Key: key, Body: compact, RetainUntil: now.AddDate(2, 0, 0)}); err != nil {
		t.Fatal(err)
	}
}

func pubOf(t *testing.T, s keys.Signer) *ecdsa.PublicKey {
	t.Helper()
	public, err := s.PublicKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pub, err := keys.ParseECPublic(public)
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

// The hour the refusals forge: the first one the notary has not sealed.
var forgedHour = now.Truncate(time.Hour).Add(4 * time.Hour)

func TestTheSealCheckerNamesTheRuleThatIsBroken(t *testing.T) {
	root, err := keys.NewLocalP384("root")
	if err != nil {
		t.Fatal(err)
	}
	stranger, _ := keys.NewLocalP384("stranger")
	delegate, _ := keys.NewLocalP384("delegate")
	wall := time.Now().UTC().Truncate(time.Second)
	hourEnd := forgedHour.Add(time.Hour)

	delegation := func(t *testing.T, s store.Store, nbf, exp time.Time, profiles, tenants []string) {
		t.Helper()
		d, err := seal.NewDelegation(seal.Thumbprint(pubOf(t, root)), seal.NewJWK(pubOf(t, delegate)), nbf, exp, profiles, tenants)
		if err != nil {
			t.Fatal(err)
		}
		payload, _ := seal.Marshal(d)
		putStatement(t, s, root, seal.TypDelegation,
			store.DelegationsPrefix+seal.Thumbprint(pubOf(t, delegate))+"/"+ulid.From(now, uint64(nbf.Unix()))+".jws", payload)
	}
	revocation := func(t *testing.T, s store.Store, signer keys.Signer, at time.Time) {
		t.Helper()
		payload, _ := seal.Marshal(&seal.Revocation{
			Iss: seal.Thumbprint(pubOf(t, signer)), Revokes: seal.Thumbprint(pubOf(t, delegate)), RevokedAt: timestamppb.New(at),
		})
		putStatement(t, s, signer, seal.TypRevocation, store.RevocationsPrefix+ulid.From(now, uint64(at.Unix()))+".jws", payload)
	}

	for _, c := range []struct {
		name string
		// rule is the rule the check must name; "" is a bucket that conforms.
		rule  string
		build func(t *testing.T, s store.Store)
	}{
		{"a seal signed by the pinned root", "", func(t *testing.T, s store.Store) {
			forge(t, s, root, "acme", forgedHour, hourEnd.Add(time.Minute), nil)
		}},
		{"a seal signed by a key nobody pinned", bucketcontract.RuleSealSignature, func(t *testing.T, s store.Store) {
			forge(t, s, stranger, "acme", forgedHour, hourEnd.Add(time.Minute), nil)
		}},
		{"a seal that chains to another seal", bucketcontract.RuleSealChain, func(t *testing.T, s store.Store) {
			forge(t, s, root, "acme", forgedHour, hourEnd.Add(time.Minute), func(x *seal.Seal) { x.Prev = strings.Repeat("a", 64) })
		}},
		{"a first seal that names a predecessor", bucketcontract.RuleSealChain, func(t *testing.T, s store.Store) {
			forge(t, s, root, "globex", forgedHour, hourEnd.Add(time.Minute), func(x *seal.Seal) { x.Prev = strings.Repeat("a", 64) })
		}},
		{"a root that is not the hour's", bucketcontract.RuleSealRoot, func(t *testing.T, s store.Store) {
			forge(t, s, root, "acme", forgedHour, hourEnd.Add(time.Minute), func(x *seal.Seal) { x.Root = strings.Repeat("b", 64) })
		}},
		{"a count that is not the hour's", bucketcontract.RuleSealRoot, func(t *testing.T, s store.Store) {
			forge(t, s, root, "acme", forgedHour, hourEnd.Add(time.Minute), func(x *seal.Seal) { x.Count = 99 })
		}},
		{"a seal made before its hour ended", bucketcontract.RuleSealPayload, func(t *testing.T, s store.Store) {
			forge(t, s, root, "acme", forgedHour, forgedHour.Add(10*time.Minute), nil)
		}},
		{"a seal for another tenant than its key says", bucketcontract.RuleSealPayload, func(t *testing.T, s store.Store) {
			forge(t, s, root, "acme", forgedHour, hourEnd.Add(time.Minute), func(x *seal.Seal) { x.Tenant = "globex" })
		}},
		{"a stray key under seals/", bucketcontract.RuleSealKey, func(t *testing.T, s store.Store) {
			putStatement(t, s, root, seal.TypSeal, "seals/security/acme/2026/09/17/stray.jws", []byte("{}"))
		}},
		{"a seal that is not a JWS", bucketcontract.RuleSealJWS, func(t *testing.T, s store.Store) {
			if err := s.Put(context.Background(), store.Object{
				Key: store.SealKey("security", "acme", forgedHour), Body: []byte("not a jws"), RetainUntil: now.AddDate(2, 0, 0),
			}); err != nil {
				t.Fatal(err)
			}
		}},
		{"an object added to a sealed hour", bucketcontract.RuleSealRoot, func(t *testing.T, s store.Store) {
			body, meta := object(t, "security", "acme", 1)
			put(t, s, keyAt("security", "acme", now.Truncate(time.Hour), 999), body, meta)
		}},
		{"an hour that is due a seal and has none", bucketcontract.RuleSealMissing, func(*testing.T, store.Store) {
			// The seals through hour 13 are there; the check is made as of a
			// time when hours 14 and 15 are due and nothing has sealed them.
		}},
		{"a delegate inside its window and scope", "", func(t *testing.T, s store.Store) {
			delegation(t, s, wall.Add(-time.Hour), wall.Add(time.Hour), []string{"security"}, []string{"acme"})
			forge(t, s, delegate, "acme", forgedHour, wall, nil)
		}},
		{"a delegate outside its window", bucketcontract.RuleSealSignature, func(t *testing.T, s store.Store) {
			delegation(t, s, wall.Add(-3*time.Hour), wall.Add(-2*time.Hour), []string{"*"}, []string{"*"})
			forge(t, s, delegate, "acme", forgedHour, wall, nil)
		}},
		{"a delegate outside its scope", bucketcontract.RuleSealSignature, func(t *testing.T, s store.Store) {
			delegation(t, s, wall.Add(-time.Hour), wall.Add(time.Hour), []string{"billing"}, []string{"*"})
			forge(t, s, delegate, "acme", forgedHour, wall, nil)
		}},
		{"a delegation of more than 25 hours", bucketcontract.RuleKeysStatement, func(t *testing.T, s store.Store) {
			delegation(t, s, wall.Add(-time.Hour), wall.Add(25*time.Hour), []string{"*"}, []string{"*"})
		}},
		{"a delegate whose key was revoked", bucketcontract.RuleSealSignature, func(t *testing.T, s store.Store) {
			delegation(t, s, wall.Add(-time.Hour), wall.Add(time.Hour), []string{"*"}, []string{"*"})
			revocation(t, s, root, wall.Add(-time.Minute))
			forge(t, s, delegate, "acme", forgedHour, wall, nil)
		}},
		{"a revocation signed by a key nobody pinned does not revoke", "", func(t *testing.T, s store.Store) {
			delegation(t, s, wall.Add(-time.Hour), wall.Add(time.Hour), []string{"*"}, []string{"*"})
			revocation(t, s, stranger, wall.Add(-time.Minute))
			forge(t, s, delegate, "acme", forgedHour, wall, nil)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			each(t, func(t *testing.T, s store.Store) {
				written(t, s, nil)
				// The roots file names the root; the notary seals as the root,
				// which is how a deployment without delegation runs.
				notarise(t, s, root, sealNow)
				c.build(t, s)

				at := sealNow
				if c.rule == bucketcontract.RuleSealMissing {
					at = sealNow.Add(3 * time.Hour)
				}
				res := checkSeals(t, s, "security", at, forgedHour, pinOf(t, root))
				switch {
				case c.rule == "" && len(res.Findings) != 0:
					t.Fatalf("a conforming bucket was refused:\n%s", res)
				case c.rule != "" && !res.Has(c.rule):
					t.Fatalf("the rule %s was not named:\n%s", c.rule, res)
				}
			})
		})
	}
}
