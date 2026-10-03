package seal_test

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v4/jwa"
	"github.com/lestrrat-go/jwx/v4/jwk"
	"github.com/lestrrat-go/jwx/v4/jws"

	"github.com/truvity/audit/internal/merkle"
	"github.com/truvity/audit/internal/seal"
	"github.com/truvity/audit/keys"
)

func signer(t *testing.T) (*keys.LocalSigner, []byte) {
	t.Helper()
	s, err := keys.NewLocalP384("seal-test")
	if err != nil {
		t.Fatal(err)
	}
	public, err := s.PublicKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s, public
}

func example() *seal.Seal {
	h := &seal.Hour{Objects: 2, Count: 3, Bytes: 1234, First: "records/security/acme/2026/09/17/10/A", Last: "records/security/acme/2026/09/17/10/B"}
	h.Root = merkle.Root([]merkle.Hash{merkle.LeafHash([]byte("a")), merkle.LeafHash([]byte("b")), merkle.LeafHash([]byte("c"))})
	return seal.NewSeal("security", "acme", time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC), h, "", time.Date(2026, 9, 17, 11, 17, 4, 999, time.UTC))
}

// A seal is a JWS any JOSE library reads: a library that shares no code with
// this package verifies it under the key's JWK and reads its header.
func TestASealIsReadByAnotherJOSELibrary(t *testing.T) {
	s, public := signer(t)
	pub, err := keys.ParseECPublic(public)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := seal.Marshal(example())
	if err != nil {
		t.Fatal(err)
	}
	compact, err := seal.Sign(context.Background(), s, seal.TypSeal, payload)
	if err != nil {
		t.Fatal(err)
	}

	got, err := jws.VerifyCompactFast(pub, compact, jwa.ES384())
	if err != nil {
		t.Fatalf("a JOSE library does not verify the seal: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("the payload it verified is not the one signed:\n%s\n%s", got, payload)
	}
	msg, err := jws.Parse(compact)
	if err != nil {
		t.Fatal(err)
	}
	h := msg.Signatures()[0].ProtectedHeaders()
	if alg, _ := h.Algorithm(); alg.String() != "ES384" {
		t.Errorf("alg %v", alg)
	}
	if typ, _ := h.Type(); typ != seal.TypSeal {
		t.Errorf("typ %v", typ)
	}

	// The kid is the RFC 7638 thumbprint, as a JOSE library computes it, and
	// the JWK Set this package writes is one a JOSE library reads.
	key, err := jwk.Import[jwk.Key](pub)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := key.Thumbprint(crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := seal.Parse(compact)
	if want := seal.Thumbprint(pub); tok.Kid != want || len(sum) != 32 {
		t.Fatalf("kid %s, thumbprint %s", tok.Kid, want)
	}
	if got := b64(sum); got != tok.Kid {
		t.Errorf("the thumbprint a JOSE library computes is %s, and the kid is %s", got, tok.Kid)
	}
	jwks, err := seal.MarshalJWKS(pub)
	if err != nil {
		t.Fatal(err)
	}
	set, err := jwk.Parse(jwks)
	if err != nil || set.Len() != 1 {
		t.Fatalf("a JOSE library does not read the JWK Set: %v\n%s", err, jwks)
	}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// The payload is the proto JSON mapping with the proto field names in
// canonical form: sorted members, 64-bit integers as strings, an empty prev
// written as "" and the times in RFC 3339.
func TestThePayloadIsCanonicalProtoJSON(t *testing.T) {
	payload, err := seal.Marshal(example())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tenant", "profile", "hour", "count", "root", "first", "last", "prev", "sealed_at", "meters"} {
		if _, ok := fields[name]; !ok {
			t.Errorf("the payload has no %s: %s", name, payload)
		}
	}
	if fields["prev"] != "" {
		t.Error("the first seal carries a prev")
	}
	if fields["count"] != "3" || fields["hour"] != "2026-09-17T10:00:00Z" || fields["sealed_at"] != "2026-09-17T11:17:04Z" {
		t.Errorf("count, hour and sealed_at: %s", payload)
	}
	again, _ := seal.Marshal(example())
	if string(again) != string(payload) {
		t.Error("the same seal marshals to different bytes")
	}
	if !strings.HasPrefix(string(payload), `{"count":"3","first":`) {
		t.Errorf("the members are not in canonical order: %s", payload)
	}
	back := &seal.Seal{}
	if err := seal.Unmarshal(payload, back); err != nil || back.GetRoot() != example().GetRoot() || back.GetMeters()["bytes"] != 1234 {
		t.Fatalf("%v %+v", err, back)
	}
}

// What a seal is not: another algorithm, another type, a signature of the wrong
// length, a statement that is not a seal.
func TestParseRefusesWhatIsNotASeal(t *testing.T) {
	s, _ := signer(t)
	good, err := seal.Sign(context.Background(), s, seal.TypSeal, mustPayload(t))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(good), ".")
	enc := func(v string) string { return b64([]byte(v)) }
	for name, c := range map[string]struct{ in, want string }{
		"two parts":                 {parts[0] + "." + parts[1], "three parts"},
		"alg none":                  {enc(`{"alg":"none","kid":"k","typ":"audit-seal+jws"}`) + "." + parts[1] + ".", "only ES384"},
		"alg ES256":                 {enc(`{"alg":"ES256","kid":"k","typ":"audit-seal+jws"}`) + "." + parts[1] + "." + parts[2], "only ES384"},
		"no kid":                    {enc(`{"alg":"ES384","typ":"audit-seal+jws"}`) + "." + parts[1] + "." + parts[2], "names no key"},
		"a short signature":         {parts[0] + "." + parts[1] + "." + parts[2][:20], "96 bytes"},
		"not base64":                {"!!." + parts[1] + "." + parts[2], "base64url"},
		"a header that is not json": {enc("not json") + "." + parts[1] + "." + parts[2], "does not parse"},
	} {
		if _, err := seal.Parse([]byte(c.in)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
	wrongType, _ := seal.Sign(context.Background(), s, seal.TypDelegation, mustPayload(t))
	if _, err := seal.ParseSeal(wrongType); err == nil || !strings.Contains(err.Error(), "type") {
		t.Errorf("a delegation was read as a seal: %v", err)
	}
	notASeal, _ := seal.Sign(context.Background(), s, seal.TypSeal, []byte(`{"tenant":"acme"}`))
	if _, err := seal.ParseSeal(notASeal); err == nil {
		t.Error("a statement with no profile, hour or root was read as a seal")
	}
}

func mustPayload(t *testing.T) []byte {
	t.Helper()
	p, err := seal.Marshal(example())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A signature made with one key does not verify under another, and a changed
// payload does not verify under the key that signed.
func TestVerifyChecksTheKeyAndThePayload(t *testing.T) {
	a, apub := signer(t)
	_, bpub := signer(t)
	compact, err := seal.Sign(context.Background(), a, seal.TypSeal, mustPayload(t))
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := seal.Parse(compact)
	pubA, _ := keys.ParseECPublic(apub)
	pubB, _ := keys.ParseECPublic(bpub)
	if err := tok.Verify(pubA); err != nil {
		t.Fatal(err)
	}
	if err := tok.Verify(pubB); err == nil {
		t.Error("a signature verified under another key")
	}
	parts := strings.Split(string(compact), ".")
	edited := parts[0] + "." + b64([]byte(`{"tenant":"x"}`)) + "." + parts[2]
	tok2, err := seal.Parse([]byte(edited))
	if err != nil {
		t.Fatal(err)
	}
	if err := tok2.Verify(pubA); err == nil {
		t.Error("a signature verified over an edited payload")
	}
}

// A key set is read only for P-384 keys, and a key's thumbprint is computed, not
// taken from the file.
func TestAKeySetHoldsOnlyP384Keys(t *testing.T) {
	_, public := signer(t)
	pub, _ := keys.ParseECPublic(public)
	set, _ := seal.MarshalJWKS(pub)
	got, err := seal.ParseJWKS(set)
	if err != nil || got[seal.Thumbprint(pub)] == nil {
		t.Fatalf("%v %v", err, got)
	}
	lying := strings.Replace(string(set), seal.Thumbprint(pub), "some-other-name", 1)
	got, err = seal.ParseJWKS([]byte(lying))
	if err != nil || got[seal.Thumbprint(pub)] == nil || got["some-other-name"] != nil {
		t.Fatalf("the file's own kid was believed: %v %v", err, got)
	}
	p256 := `{"keys":[{"kty":"EC","crv":"P-256","x":"f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU","y":"x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0"}]}`
	if _, err := seal.ParseJWKS([]byte(p256)); err == nil || !strings.Contains(err.Error(), "P-384") {
		t.Errorf("a P-256 key was read: %v", err)
	}
	offCurve := strings.Replace(string(set), `"y": "`, `"y": "AA`, 1)
	if _, err := seal.ParseJWKS([]byte(offCurve)); err == nil {
		t.Error("a key off the curve was read")
	}
}

// The worked example in the bucket contract is a real seal pair, committed in
// testdata: a reader with the JWK Set and the two files checks everything the
// contract says about them, with a JOSE library and nothing of this package's
// signing.
func TestTheWorkedExampleOfTheContractVerifies(t *testing.T) {
	jwksBody, err := os.ReadFile("testdata/example.jwks")
	if err != nil {
		t.Fatal(err)
	}
	set, err := seal.ParseJWKS(jwksBody)
	if err != nil || len(set) != 1 {
		t.Fatalf("%v %v", err, set)
	}
	var pub *ecdsa.PublicKey
	var kid string
	for k, p := range set {
		kid, pub = k, p
	}
	read := func(name string) []byte {
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return []byte(strings.TrimSpace(string(b)))
	}
	first, second := read("example-10.jws"), read("example-11.jws")
	for _, compact := range [][]byte{first, second} {
		if _, err := jws.VerifyCompactFast(pub, compact, jwa.ES384()); err != nil {
			t.Fatalf("a JOSE library does not verify the example: %v", err)
		}
	}
	a, err := seal.ParseSeal(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := seal.ParseSeal(second)
	if err != nil {
		t.Fatal(err)
	}
	if a.Token.Kid != kid || b.Token.Kid != kid {
		t.Errorf("kid %s and %s, thumbprint %s", a.Token.Kid, b.Token.Kid, kid)
	}
	// The first seal's root is the n = 3 vector, and the second is the empty hour.
	if a.Seal.GetRoot() != "8be871f13785b4c81a1700459c76ac2b3ae2caebb7876c376e223c6adff98c47" || a.Seal.GetCount() != 3 {
		t.Errorf("the first seal: %+v", a.Seal)
	}
	if b.Seal.GetRoot() != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" || b.Seal.GetCount() != 0 {
		t.Errorf("the second seal: %+v", b.Seal)
	}
	if b.Seal.GetPrev() != seal.Hash(first) {
		t.Errorf("the second seal chains to %s, the first hashes to %s", b.Seal.GetPrev(), seal.Hash(first))
	}
}
