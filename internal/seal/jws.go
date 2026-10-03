// Package seal is the format of the three signed statements of
// docs/reference/bucket-contract.md — seals, delegations and revocations —
// the keys that sign them, and how a verifier decides whether to believe one.
//
// Each statement is a JWS in compact serialisation (RFC 7515), algorithm ES384,
// so that an auditor can read one with any JOSE library. This package writes
// them with the standard library and the keys.Signer, and a test reads them back
// with a JOSE library that shares no code with it.
package seal

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/truvity/audit/keys"
)

// The algorithm and the three types of statement.
const (
	Alg           = "ES384"
	TypSeal       = "audit-seal+jws"
	TypDelegation = "audit-delegation+jws"
	TypRevocation = "audit-revocation+jws"
)

// coordinate is the length of a P-384 coordinate and of each half of an ES384
// signature, in bytes.
const coordinate = 48

var b64 = base64.RawURLEncoding

// JWK is a public key as RFC 7517 writes it. Only P-384 keys are written.
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	Kid string `json:"kid,omitempty"`
	Alg string `json:"alg,omitempty"`
	Use string `json:"use,omitempty"`
}

// NewJWK is the JWK of a P-384 public key, with its thumbprint as the kid.
func NewJWK(pub *ecdsa.PublicKey) JWK {
	x, y := coordinates(pub)
	return JWK{Kty: "EC", Crv: "P-384", X: x, Y: y, Kid: Thumbprint(pub), Alg: Alg, Use: "sig"}
}

// coordinates are the base64url of a P-384 public key's x and y, each a fixed
// 48 bytes, as RFC 7518 section 6.2.1 writes them.
func coordinates(pub *ecdsa.PublicKey) (x, y string) {
	point, err := pub.Bytes() // 0x04 || x || y, uncompressed
	if err != nil || len(point) != 1+2*coordinate {
		// Not a P-384 point: every key that reaches here was parsed, so this is
		// a key made by hand, and it names nothing.
		return "", ""
	}
	return b64.EncodeToString(point[1 : 1+coordinate]), b64.EncodeToString(point[1+coordinate:])
}

// Key reads the public key a JWK holds, which must be on P-384.
func (j JWK) Key() (*ecdsa.PublicKey, error) {
	if j.Kty != "EC" || j.Crv != "P-384" {
		return nil, fmt.Errorf("seal: a key of type %s on %s is not a seal key: they are EC on P-384", j.Kty, j.Crv)
	}
	x, err := b64.DecodeString(j.X)
	if err != nil || len(x) != coordinate {
		return nil, errors.New("seal: the key's x is not a P-384 coordinate")
	}
	y, err := b64.DecodeString(j.Y)
	if err != nil || len(y) != coordinate {
		return nil, errors.New("seal: the key's y is not a P-384 coordinate")
	}
	// An off-curve point is no key: it is refused here, and not left to verify
	// everything as false while a thumbprint of it looks like any other.
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P384(), append(append([]byte{4}, x...), y...))
	if err != nil {
		return nil, fmt.Errorf("seal: the key is not a point on P-384: %w", err)
	}
	return pub, nil
}

// Thumbprint is the RFC 7638 thumbprint of a P-384 public key: the SHA-256 of
// its required members in lexical order, base64url. It is the key's name in a
// seal's header, in a delegation and in a verifier's pins.
func Thumbprint(pub *ecdsa.PublicKey) string {
	x, y := coordinates(pub)
	sum := sha256.Sum256([]byte(`{"crv":"P-384","kty":"EC","x":"` + x + `","y":"` + y + `"}`))
	return b64.EncodeToString(sum[:])
}

// JWKS is a JWK Set.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// MarshalJWKS writes a set of keys, each named by its thumbprint.
func MarshalJWKS(pubs ...*ecdsa.PublicKey) ([]byte, error) {
	set := JWKS{Keys: make([]JWK, 0, len(pubs))}
	for _, pub := range pubs {
		set.Keys = append(set.Keys, NewJWK(pub))
	}
	return json.MarshalIndent(set, "", "  ")
}

// ParseJWKS reads a JWK Set into its keys, by thumbprint, which it computes
// from the key and never takes from the file's own `kid`. A key that is not a
// P-384 key is an error naming it: a verifier that skipped one silently could
// not say why a seal's key was not found.
func ParseJWKS(raw []byte) (map[string]*ecdsa.PublicKey, error) {
	var set JWKS
	if err := json.Unmarshal(raw, &set); err != nil {
		return nil, fmt.Errorf("seal: the key set does not parse: %w", err)
	}
	out := make(map[string]*ecdsa.PublicKey, len(set.Keys))
	for i, jwk := range set.Keys {
		pub, err := jwk.Key()
		if err != nil {
			return nil, fmt.Errorf("seal: key %d of the set: %w", i+1, err)
		}
		out[Thumbprint(pub)] = pub
	}
	return out, nil
}

// header is a JWS's protected header, in the order the members sort in.
type header struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

// Token is a JWS taken apart, not yet checked.
type Token struct {
	Alg, Kid, Typ string
	Payload       []byte
	// Bytes is the compact serialisation as it was read. A seal's successor
	// names the SHA-256 of exactly these bytes.
	Bytes []byte

	input     []byte
	signature []byte
}

// Sign makes the compact JWS of a payload under a signer, naming the signer's
// key by thumbprint. The signature comes back from the signer ASN.1 encoded
// and goes into the JWS as the two integers side by side, which is what ES384
// is.
func Sign(ctx context.Context, signer keys.Signer, typ string, payload []byte) ([]byte, error) {
	public, err := signer.PublicKey(ctx)
	if err != nil {
		return nil, err
	}
	pub, err := keys.ParseECPublic(public)
	if err != nil {
		return nil, err
	}
	head, err := json.Marshal(header{Alg: Alg, Kid: Thumbprint(pub), Typ: typ})
	if err != nil {
		return nil, err
	}
	input := b64.EncodeToString(head) + "." + b64.EncodeToString(payload)
	der, err := signer.Sign(ctx, []byte(input))
	if err != nil {
		return nil, err
	}
	raw, err := rawSignature(der)
	if err != nil {
		return nil, err
	}
	// A signer that signs with another key than the one it names would
	// produce a statement nobody could verify; better to find out here.
	if err := verifyRaw(pub, []byte(input), raw); err != nil {
		return nil, errors.New("seal: the signature the signer returned does not verify under its own public key")
	}
	return []byte(input + "." + b64.EncodeToString(raw)), nil
}

// rawSignature turns an ASN.1 ECDSA signature into the r and s of JWS: each
// as a fixed 48 bytes, big-endian.
func rawSignature(der []byte) ([]byte, error) {
	var sig struct{ R, S *big.Int }
	rest, err := asn1.Unmarshal(der, &sig)
	if err != nil || len(rest) != 0 || sig.R == nil || sig.S == nil || sig.R.Sign() <= 0 || sig.S.Sign() <= 0 {
		return nil, errors.New("seal: the signer did not return an ASN.1 ECDSA signature")
	}
	if sig.R.BitLen() > 8*coordinate || sig.S.BitLen() > 8*coordinate {
		return nil, errors.New("seal: the signature is too large for P-384")
	}
	out := make([]byte, 2*coordinate)
	sig.R.FillBytes(out[:coordinate])
	sig.S.FillBytes(out[coordinate:])
	return out, nil
}

func verifyRaw(pub *ecdsa.PublicKey, input, raw []byte) error {
	if len(raw) != 2*coordinate {
		return fmt.Errorf("the signature is %d bytes and an ES384 signature is %d", len(raw), 2*coordinate)
	}
	sum := sha512.Sum384(input)
	r, s := new(big.Int).SetBytes(raw[:coordinate]), new(big.Int).SetBytes(raw[coordinate:])
	if !ecdsa.Verify(pub, sum[:], r, s) {
		return errors.New("the signature does not check out")
	}
	return nil
}

// Parse reads a compact JWS and checks its shape: three parts, a protected
// header that is ES384 and names a key, and a signature of the right length.
// It does not check the signature: that needs the key, which is the verifier's
// to choose.
func Parse(compact []byte) (*Token, error) {
	parts := strings.Split(string(compact), ".")
	if len(parts) != 3 {
		return nil, errors.New("seal: not a compact JWS: it is not three parts")
	}
	head, err := b64.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("seal: the protected header is not base64url")
	}
	var h header
	if err := json.Unmarshal(head, &h); err != nil {
		return nil, fmt.Errorf("seal: the protected header does not parse: %w", err)
	}
	if h.Alg != Alg {
		return nil, fmt.Errorf("seal: the algorithm is %q and only %s is accepted", h.Alg, Alg)
	}
	if h.Kid == "" {
		return nil, errors.New("seal: the protected header names no key")
	}
	payload, err := b64.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("seal: the payload is not base64url")
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil || len(sig) != 2*coordinate {
		return nil, fmt.Errorf("seal: the signature is not %d bytes of base64url", 2*coordinate)
	}
	return &Token{
		Alg: h.Alg, Kid: h.Kid, Typ: h.Typ, Payload: payload,
		Bytes: append([]byte(nil), compact...), input: []byte(parts[0] + "." + parts[1]), signature: sig,
	}, nil
}

// Verify checks the signature under a key.
func (t *Token) Verify(pub *ecdsa.PublicKey) error { return verifyRaw(pub, t.input, t.signature) }

// Hash is what a seal's successor names it by: the lower-case hex SHA-256 of
// the seal's bytes, as stored.
func Hash(stored []byte) string {
	sum := sha256.Sum256(stored)
	return hex.EncodeToString(sum[:])
}
