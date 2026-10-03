package keys

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

// Signer signs what the notary vouches for: a seal, and the statements about
// the keys that sign them.
//
// A seal is signed ES384: the key is P-384, the signature is ECDSA over a
// SHA-384 of the message, and Sign returns it ASN.1 encoded, as KMS and
// transit do. The JWS wants the two integers side by side; internal/seal turns
// one into the other. An older key type (ed25519, P-256) still signs and
// verifies here, for the callers that predate seals.
//
// The private half never has to leave the provider that holds it: a deployment
// signs with a managed key and publishes only the public half. What an auditor
// needs is that public half and a copy of the verifier, and then the chain can
// be checked without trusting the operator, which is the whole point of signing
// it.
type Signer interface {
	// Sign returns a signature over a message.
	Sign(ctx context.Context, message []byte) ([]byte, error)
	// PublicKey returns the verifying half, PEM encoded.
	PublicKey(ctx context.Context) ([]byte, error)
	// KeyID names the key for an operator's log. What a seal names as its
	// signer is the key's RFC 7638 thumbprint, which is computed from the public
	// half and so cannot disagree with it.
	KeyID() string
}

// Verify checks a signature against a PEM public key. It is separate from
// Signer because whoever verifies has no signer and should need none.
func Verify(publicKeyPEM, message, signature []byte) error {
	block, _ := pem.Decode(publicKeyPEM)
	if block == nil {
		return errors.New("keys: the public key is not PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("keys: the public key does not parse: %w", err)
	}
	switch pub := parsed.(type) {
	case ed25519.PublicKey:
		if !ed25519.Verify(pub, message, signature) {
			return errors.New("keys: the signature does not check out")
		}
		return nil
	case *ecdsa.PublicKey:
		// ECDSA signs a hash of the message — SHA-256 on P-256 and SHA-384 on
		// P-384, the pairs of ES256 and ES384 — which is how a managed key
		// signs a body larger than it will take whole. The signature is ASN.1,
		// as KMS returns it.
		sum, err := ecdsaDigest(pub.Curve, message)
		if err != nil {
			return err
		}
		if !ecdsa.VerifyASN1(pub, sum, signature) {
			return errors.New("keys: the signature does not check out")
		}
		return nil
	default:
		return fmt.Errorf("keys: %T is not a key this build verifies", parsed)
	}
}

// ecdsaDigest is the hash an ECDSA key of this curve signs: the one the JOSE
// algorithm of the curve names.
func ecdsaDigest(curve elliptic.Curve, message []byte) ([]byte, error) {
	switch curve {
	case elliptic.P256():
		sum := sha256.Sum256(message)
		return sum[:], nil
	case elliptic.P384():
		sum := sha512.Sum384(message)
		return sum[:], nil
	}
	return nil, fmt.Errorf("keys: only P-256 and P-384 ECDSA keys are verified, not %s", curve.Params().Name)
}

// ParseECPublic reads a PEM public key that must be an ECDSA key on P-384, the
// key of a seal. It names what it found when it is anything else.
func ParseECPublic(publicKeyPEM []byte) (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode(publicKeyPEM)
	if block == nil {
		return nil, errors.New("keys: the public key is not PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("keys: the public key does not parse: %w", err)
	}
	pub, ok := parsed.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P384() {
		return nil, fmt.Errorf("keys: a seal is signed with a P-384 key (ES384), and this is %s", describe(parsed))
	}
	return pub, nil
}

func describe(key any) string {
	if pub, ok := key.(*ecdsa.PublicKey); ok {
		return "an ECDSA key on " + pub.Curve.Params().Name
	}
	return fmt.Sprintf("a %T", key)
}

// LocalSigner signs with a key on this machine.
//
// It is for tests, for the conformance suite, and for a deployment small enough
// that the signing key living beside the archive is an accepted risk. It is
// worth being plain about that risk: whoever can write the archive can also
// sign a seal for what they wrote, so a local signer proves that objects have
// not changed since they were signed and not that the operator did not choose
// what to sign.
type LocalSigner struct {
	id      string
	private crypto.Signer
}

// NewLocalSigner returns a signer holding a generated ed25519 key. It is the
// key of the signers that predate seals; a seal is signed with NewLocalP384.
func NewLocalSigner(id string) (*LocalSigner, error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("keys: %w", err)
	}
	return &LocalSigner{id: nameOr(id), private: private}, nil
}

// NewLocalP384 returns a signer holding a generated P-384 key: what signs a
// seal (ES384).
func NewLocalP384(id string) (*LocalSigner, error) {
	private, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("keys: %w", err)
	}
	return &LocalSigner{id: nameOr(id), private: private}, nil
}

func nameOr(id string) string {
	if id == "" {
		return "local"
	}
	return id
}

// LoadLocalSigner reads a private key in PEM form: PKCS#8 (ed25519, P-256 or
// P-384) or SEC 1 ("EC PRIVATE KEY", as `openssl ecparam -genkey` writes it).
func LoadLocalSigner(id string, pemBytes []byte) (*LocalSigner, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("keys: the private key is not PEM")
	}
	var parsed any
	var err error
	if block.Type == "EC PRIVATE KEY" {
		parsed, err = x509.ParseECPrivateKey(block.Bytes)
	} else {
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	}
	if err != nil {
		return nil, fmt.Errorf("keys: the private key does not parse: %w", err)
	}
	switch k := parsed.(type) {
	case ed25519.PrivateKey:
		return &LocalSigner{id: nameOr(id), private: k}, nil
	case *ecdsa.PrivateKey:
		if k.Curve != elliptic.P256() && k.Curve != elliptic.P384() {
			return nil, fmt.Errorf("keys: a signing key is on P-256 or P-384, not %s", k.Curve.Params().Name)
		}
		return &LocalSigner{id: nameOr(id), private: k}, nil
	}
	return nil, fmt.Errorf("keys: %T is not a key this build signs with", parsed)
}

// LoadLocalSignerFile reads a private key from a file.
func LoadLocalSignerFile(id, path string) (*LocalSigner, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("keys: %w", err)
	}
	return LoadLocalSigner(id, body)
}

// Sign implements Signer.
func (s *LocalSigner) Sign(_ context.Context, message []byte) ([]byte, error) {
	if k, ok := s.private.(*ecdsa.PrivateKey); ok {
		sum, err := ecdsaDigest(k.Curve, message)
		if err != nil {
			return nil, err
		}
		return ecdsa.SignASN1(rand.Reader, k, sum)
	}
	return s.private.Sign(nil, message, crypto.Hash(0))
}

// PublicKey implements Signer.
func (s *LocalSigner) PublicKey(_ context.Context) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(s.private.Public())
	if err != nil {
		return nil, fmt.Errorf("keys: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// PrivateKey returns the signing key in PEM form (PKCS#8), so a test or a small
// deployment can keep it somewhere and load it again.
func (s *LocalSigner) PrivateKey() ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(s.private)
	if err != nil {
		return nil, fmt.Errorf("keys: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// KeyID implements Signer.
func (s *LocalSigner) KeyID() string { return s.id }
