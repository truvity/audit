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
