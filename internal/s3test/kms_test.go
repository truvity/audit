package s3test_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"

	"github.com/truvity/audit/internal/digest"
	"github.com/truvity/audit/internal/s3test"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/store"
	"github.com/truvity/audit/store/storetest"
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

// A chain signed in KMS verifies with only the public half it exports, and a
// digest altered after signing does not.
//
// This is the signer that answers the local one's limit: the private key never
// leaves KMS, so writing the archive and vouching for it are different
// privileges.
func TestAChainSignedInKMSVerifiesWithItsPublicHalf(t *testing.T) {
	c := kmsClient(t)
	signer := &keys.KMSSigner{Client: c, Key: newKey(t, c, types.KeySpecEccNistP256)}
	ctx := context.Background()
	public, err := signer.PublicKey(ctx)
	if err != nil {
		t.Fatal(err)
	}

	s := storetest.NewMemory()
	window := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return window.Add(30 * time.Minute) }
	if err := s.Put(ctx, store.Object{
		Key:  "profile=security/tenant=acme/year=2026/month=09/day=17/a.ndjson.zst",
		Body: []byte("one"), RetainUntil: window.AddDate(1, 0, 0),
	}); err != nil {
		t.Fatal(err)
	}
	builder := &digest.Builder{Store: s, Signer: signer}
	for _, start := range []time.Time{window, window.Add(time.Hour)} {
		d, err := builder.Build(ctx, "security", "profile=security", start, start.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := builder.Write(ctx, d, start.AddDate(1, 0, 0)); err != nil {
			t.Fatal(err)
		}
	}

	verifier := &digest.Verifier{Store: s, PublicKeyPEM: public}
	report, err := verifier.Verify(ctx, "security", window, window.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK() {
		t.Fatalf("a chain KMS signed does not verify:\n%s", report)
	}

	// Alter what the first digest says after the fact — one object's size —
	// and its signature no longer holds. (Reformatting it would change nothing:
	// the signature is over the canonical form, as it should be.)
	key := digest.Key("security", window)
	d, err := digest.Read(ctx, s, key)
	if err != nil {
		t.Fatal(err)
	}
	d.Objects[0].Size++
	altered, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	s.Replace(key, altered)
	report, err = verifier.Verify(ctx, "security", window, window.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if report.OK() {
		t.Fatal("an altered digest still verified")
	}
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
