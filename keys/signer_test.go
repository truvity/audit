package keys_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/truvity/audit/keys"
)

// A P-384 key signs ES384: ECDSA over SHA-384, ASN.1, and the public half it
// exports verifies it and nothing else.
func TestALocalP384SignerSignsES384(t *testing.T) {
	ctx := context.Background()
	s, err := keys.NewLocalP384("seal-test")
	if err != nil {
		t.Fatal(err)
	}
	message := []byte("eyJhbGciOiJFUzM4NCJ9.e30")
	signature, err := s.Sign(ctx, message)
	if err != nil {
		t.Fatal(err)
	}
	public, err := s.PublicKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.Verify(public, message, signature); err != nil {
		t.Fatalf("a P-384 signature did not verify: %v", err)
	}
	if err := keys.Verify(public, append(message, '.'), signature); err == nil {
		t.Fatal("a signature verified over a message it was not made for")
	}
	other, _ := keys.NewLocalP384("other")
	otherPublic, _ := other.PublicKey(ctx)
	if err := keys.Verify(otherPublic, message, signature); err == nil {
		t.Fatal("a signature verified under another key")
	}
	if _, err := keys.ParseECPublic(public); err != nil {
		t.Fatalf("the seal key's public half was refused: %v", err)
	}
}

// A key file is PKCS#8 or SEC 1, as `openssl ecparam -genkey -name secp384r1`
// writes it, and a signer loaded from it signs as the one that was saved.
func TestAP384KeyFileLoadsInEitherEncoding(t *testing.T) {
	ctx := context.Background()
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sec1, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	want := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})
	for name, block := range map[string]*pem.Block{
		"sec1":  {Type: "EC PRIVATE KEY", Bytes: sec1},
		"pkcs8": {Type: "PRIVATE KEY", Bytes: pkcs8},
	} {
		s, err := keys.LoadLocalSigner("file", pem.EncodeToMemory(block))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, _ := s.PublicKey(ctx)
		if string(got) != string(want) {
			t.Fatalf("%s: a different public key", name)
		}
		signature, err := s.Sign(ctx, []byte("m"))
		if err != nil {
			t.Fatal(err)
		}
		if err := keys.Verify(got, []byte("m"), signature); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// The seal key is P-384 and nothing else: a P-256 or an ed25519 public half is
// refused by name.
func TestTheSealKeyMustBeP384(t *testing.T) {
	ctx := context.Background()
	ed, _ := keys.NewLocalSigner("ed")
	edPublic, _ := ed.PublicKey(ctx)
	if _, err := keys.ParseECPublic(edPublic); err == nil || !strings.Contains(err.Error(), "P-384") {
		t.Fatalf("an ed25519 key was accepted as a seal key: %v", err)
	}
	p256, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&p256.PublicKey)
	if _, err := keys.ParseECPublic(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})); err == nil ||
		!strings.Contains(err.Error(), "P-256") {
		t.Fatalf("a P-256 key was accepted as a seal key: %v", err)
	}
}
