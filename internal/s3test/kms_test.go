package s3test_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"

	"github.com/truvity/audit/internal/s3test"
	"github.com/truvity/audit/internal/seal"
	"github.com/truvity/audit/keys"
)

// kmsClient is KMS on the same LocalStack endpoint the S3 tests use.
func kmsClient(t *testing.T) *kms.Client {
	t.Helper()
	endpoint := os.Getenv(s3test.URLEnv)
	if endpoint == "" {
		t.Skip("set " + s3test.URLEnv + " to run the KMS tests (a LocalStack endpoint)")
	}
	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion("eu-central-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")))
	if err != nil {
		t.Fatal(err)
	}
	return kms.NewFromConfig(cfg, func(o *kms.Options) { o.BaseEndpoint = aws.String(endpoint) })
}

func newKey(t *testing.T, c *kms.Client, spec types.KeySpec) string {
	t.Helper()
	out, err := c.CreateKey(context.Background(), &kms.CreateKeyInput{
		KeySpec: spec, KeyUsage: types.KeyUsageTypeSignVerify,
	})
	if err != nil {
		t.Fatal(err)
	}
	return aws.ToString(out.KeyMetadata.KeyId)
}

// A key of another kind is refused when its public half is asked for, before
// anything is signed with it, rather than producing signatures the verifier
// cannot check.
func TestAKMSKeyOfTheWrongKindIsRefused(t *testing.T) {
	c := kmsClient(t)
	signer := &keys.KMSSigner{Client: c, Key: newKey(t, c, types.KeySpecRsa2048)}
	if _, err := signer.PublicKey(context.Background()); err == nil || !strings.Contains(err.Error(), "ECC_NIST_P256") {
		t.Fatalf("an RSA key was accepted: %v", err)
	}
}

// A KMS ECC_NIST_P384 key signs ES384: the signer asks for a SHA-384 digest to
// be signed ECDSA_SHA_384, and what comes back verifies with the exported
// public half and nothing else — which is what a seal's verifier has.
func TestAKMSP384KeySignsES384(t *testing.T) {
	c := kmsClient(t)
	ctx := context.Background()
	signer := &keys.KMSSigner{Client: c, Key: newKey(t, c, types.KeySpecEccNistP384)}
	message := []byte("eyJhbGciOiJFUzM4NCJ9.eyJ0ZW5hbnQiOiJhY21lIn0")
	signature, err := signer.Sign(ctx, message)
	if err != nil {
		t.Fatal(err)
	}
	public, err := signer.PublicKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keys.ParseECPublic(public); err != nil {
		t.Fatal(err)
	}
	if err := keys.Verify(public, message, signature); err != nil {
		t.Fatalf("a KMS P-384 signature did not verify: %v", err)
	}
	if err := keys.Verify(public, append(message, ' '), signature); err == nil {
		t.Fatal("a signature verified over a message it was not made for")
	}

	// A seal made with it is a JWS a JOSE reader accepts.
	compact, err := seal.Sign(ctx, signer, seal.TypSeal, []byte(`{"tenant":"acme"}`))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := seal.Parse(compact)
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := keys.ParseECPublic(public)
	if err := tok.Verify(pub); err != nil || tok.Kid != seal.Thumbprint(pub) {
		t.Fatalf("the seal does not verify under the key it names: %v", err)
	}
}
