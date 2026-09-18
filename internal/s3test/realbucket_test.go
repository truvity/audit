package s3test

import (
	"context"
	"crypto/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/truvity/audit/store"
	"github.com/truvity/audit/store/s3store"
)

// RealBucketEnv names an existing bucket with Object Lock, in real S3, that the
// retention checks may write to. No emulator implements PutObjectRetention, so
// the two claims the retention addendum rests on — a lock can be lengthened,
// and compliance mode refuses to shorten it — are checked only here.
//
// Each run leaves one small object locked for two days. Point it at a sandbox
// bucket, with credentials from the ordinary AWS chain, and run it on demand:
//
//	AUDIT_S3_REAL_BUCKET=<bucket> go test ./internal/s3test -run RealBucket
const RealBucketEnv = "AUDIT_S3_REAL_BUCKET"

func TestRealBucketLengthensALockAndRefusesToShortenIt(t *testing.T) {
	bucket := os.Getenv(RealBucketEnv)
	if bucket == "" {
		t.Skip("set " + RealBucketEnv + " to an Object-Locked sandbox bucket to run the retention checks against real S3")
	}
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := s3store.FromConfig(cfg, s3store.Options{Bucket: bucket, Prefix: "audit-retention-check"})
	if err != nil {
		t.Fatal(err)
	}
	// Whole seconds, because that is what S3 keeps.
	now := time.Now().UTC().Truncate(time.Second)
	first, longer := now.Add(24*time.Hour), now.Add(48*time.Hour)
	key := "run/" + strings.ToLower(rand.Text()[:12]) + ".txt"
	if err := archive.Put(ctx, store.Object{
		Key: key, Body: []byte("retention check"), RetainUntil: first, ContentType: "text/plain",
	}); err != nil {
		t.Fatalf("putting a locked object: %v", err)
	}
	if got := lockOf(ctx, t, archive, key); !got.Equal(first) {
		t.Fatalf("the object is locked until %s, want %s", got, first)
	}

	if err := archive.ExtendRetention(ctx, key, longer); err != nil {
		t.Fatalf("lengthening the lock: %v", err)
	}
	if got := lockOf(ctx, t, archive, key); !got.Equal(longer) {
		t.Fatalf("after lengthening the object is locked until %s, want %s", got, longer)
	}

	// Through the store, and past it with the raw client, so that it is the
	// bucket refusing and not a check in our own code.
	if err := archive.ExtendRetention(ctx, key, first); err == nil {
		t.Fatal("the bucket shortened a compliance lock through the store")
	}
	client := s3.NewFromConfig(cfg)
	_, err = client.PutObjectRetention(ctx, &s3.PutObjectRetentionInput{
		Bucket: aws.String(bucket), Key: aws.String("audit-retention-check/" + key),
		Retention: &types.ObjectLockRetention{
			Mode: types.ObjectLockRetentionModeCompliance, RetainUntilDate: aws.Time(first),
		},
	})
	if err == nil {
		t.Fatal("the bucket shortened a compliance lock when asked directly")
	}
	if got := lockOf(ctx, t, archive, key); !got.Equal(longer) {
		t.Fatalf("after the refused shortening the object is locked until %s, want %s", got, longer)
	}
}

func lockOf(ctx context.Context, t *testing.T, s store.Store, key string) time.Time {
	t.Helper()
	entry, err := s.Head(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	return entry.RetainUntil.UTC()
}
