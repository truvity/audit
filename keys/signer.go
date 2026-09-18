package keys

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

// Signer signs the digest chain.
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
	// KeyID names the key in every digest it signs, so a verifier can tell
	// which key to check a digest against after a key has been replaced.
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
		// ECDSA over P-256 signs a SHA-256 of the message, which is how a
		// managed key signs a body larger than it will take whole. The
		// signature is ASN.1, as KMS returns it.
		if pub.Curve != elliptic.P256() {
			return errors.New("keys: only P-256 ECDSA keys are verified")
		}
		sum := sha256.Sum256(message)
		if !ecdsa.VerifyASN1(pub, sum[:], signature) {
			return errors.New("keys: the signature does not check out")
		}
		return nil
	default:
		return fmt.Errorf("keys: %T is not a key this build verifies", parsed)
	}
}

// LocalSigner signs with a key on this machine.
//
// It is for tests, for the conformance suite, and for a deployment small enough
// that the signing key living beside the archive is an accepted risk. It is
// worth being plain about that risk: whoever can write the archive can also
// sign a digest for what they wrote, so a local signer proves that objects have
// not changed since they were signed and not that the operator did not choose
// what to sign.
type LocalSigner struct {
	id      string
	private ed25519.PrivateKey
}

// NewLocalSigner returns a signer holding a generated key.
func NewLocalSigner(id string) (*LocalSigner, error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("keys: %w", err)
	}
	if id == "" {
		id = "local"
	}
	return &LocalSigner{id: id, private: private}, nil
}

// LoadLocalSigner reads a PKCS#8 private key in PEM form.
func LoadLocalSigner(id string, pemBytes []byte) (*LocalSigner, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("keys: the private key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("keys: the private key does not parse: %w", err)
	}
	private, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("keys: %T is not a key this build signs with", parsed)
	}
	if id == "" {
		id = "local"
	}
	return &LocalSigner{id: id, private: private}, nil
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

// PrivateKey returns the signing key in PEM form, so a test or a small
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
