// Package s3test gives a test a real S3 to walk.
//
// The archive walks are the part of this repository that has been wrong twice,
// and both times the tests passed: a digest that covered one tenant, and a
// listing that stopped at the first thousand keys. Both hid behind
// storetest.Memory, which returns everything, in any layout, on one page. A
// double kinder than the thing it stands in for is not a test.
//
// So these tests run against LocalStack, which pages ListObjectsV2 at a
// thousand like AWS and honours Delimiter and continuation tokens the same way.
// What it does not do is enforce compliance retention: a delete may succeed
// here where AWS would refuse. Nothing in this package may therefore claim that
// a lock holds — that property belongs to the tamper tests over the memory
// store and to a check against a real bucket. What is proved here is that the
// requests are right and that the walks are complete.
package s3test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/truvity/audit/store"
	"github.com/truvity/audit/store/s3store"
)

// URLEnv names the S3 endpoint the tests run against.
//
// It is an endpoint, not a product: LocalStack, MinIO or a real bucket all
// satisfy it. Which one a deployment or CI points at is a choice that can
// change without touching a test, which matters because the licence and the
// maintenance status of every S3 fake are somebody else's to change.
const URLEnv = "AUDIT_S3_URL"

// region is the one these tests create buckets in.
const region = "eu-central-1"

// Open returns a store over a bucket of this test's own, emptied when it ends.
//
// Object Lock has to be asked for when a bucket is created and cannot be added
// afterwards, so the bucket is made with it: what these tests exercise is the
// writer's real path, which always names a lock mode.
func Open(t *testing.T, lock bool) store.Store {
	t.Helper()
	endpoint := os.Getenv(URLEnv)
	if endpoint == "" {
		t.Skip("set " + URLEnv + " to run the S3 tests (a LocalStack endpoint)")
	}

	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	// Path style: a bucket per test means a hostname per test, and no DNS here
	// would resolve it.
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})

	bucket := bucketName(t.Name())
	// Every region but us-east-1 needs the constraint stated. S3 rejects a
	// create without it as an "unspecified location constraint", which is the
	// kind of rule a fake would let pass and the real thing does not.
	in := &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
		CreateBucketConfiguration: &types.CreateBucketConfiguration{
			LocationConstraint: types.BucketLocationConstraint(region),
		},
	}
	if lock {
		in.ObjectLockEnabledForBucket = aws.Bool(true)
	}
	if _, err := client.CreateBucket(ctx, in); err != nil {
		t.Fatalf("s3test: creating %s: %v", bucket, err)
	}
	t.Cleanup(func() { empty(client, bucket) })

	built, err := s3store.New(client, s3store.Options{Bucket: bucket, Unlocked: !lock})
	if err != nil {
		t.Fatal(err)
	}
	return built
}

// empty removes a bucket's objects and the bucket. It is best effort: a test
// that leaves a bucket behind is untidy, not wrong, and failing the cleanup
// would hide whatever the test actually found.
func empty(client *s3.Client, bucket string) {
	ctx := context.Background()
	var token *string
	for {
		out, err := client.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{
			Bucket: aws.String(bucket), KeyMarker: token,
		})
		if err != nil {
			return
		}
		for _, v := range out.Versions {
			_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: aws.String(bucket), Key: v.Key, VersionId: v.VersionId,
				BypassGovernanceRetention: aws.Bool(true),
			})
		}
		for _, m := range out.DeleteMarkers {
			_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: aws.String(bucket), Key: m.Key, VersionId: m.VersionId,
			})
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		token = out.NextKeyMarker
	}
	_, _ = client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
}

// bucketName turns a test's name into one S3 will take: lower case, letters,
// digits and hyphens.
func bucketName(name string) string {
	var b strings.Builder
	b.WriteString("audit-")
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	if len(out) > 63 {
		out = out[:63]
	}
	return strings.Trim(out, "-")
}

// Fill writes n objects under one profile and tenant on one day, so that a test
// can cross a listing page.
func Fill(t *testing.T, s store.Store, profile, tenant, day string, n int) []string {
	t.Helper()
	ctx := context.Background()
	keys := make([]string, 0, n)
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("profile=%s/tenant=%s/%s/%06d.ndjson.zst", profile, tenant, day, i)
		if err := s.Put(ctx, store.Object{Key: key, Body: []byte("{}")}); err != nil {
			t.Fatalf("s3test: %s: %v", key, err)
		}
		keys = append(keys, key)
	}
	return keys
}
