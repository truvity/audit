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
//
// What it does and does not enforce was measured rather than assumed, because
// the first version of this comment guessed and guessed wrong. It refuses to
// delete a version held by a compliance retention, which is the guarantee the
// whole archive rests on and is asserted here. It does not implement
// PutObjectRetention at all — it answers MethodNotAllowed where AWS answers
// AccessDenied — so nothing here may claim that a retention cannot be
// shortened. That claim is checked against real S3 instead, on demand, by the
// test in realbucket_test.go.
package s3test

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

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

// Open returns a store over a bucket of this test's own.
//
// Object Lock has to be asked for when a bucket is created and cannot be added
// afterwards, so with lock the bucket is made with it and the store writes in
// compliance mode: the writer's path on the record tier. Without, the bucket
// has no Object Lock and the store writes with lock mode none: the writer's
// path on the attested tier, and the exports bucket's shape. Both are real
// paths, and the harness serves both so that neither is the kinder double.
//
// The bucket's name ends in a random suffix, because a locked bucket cannot be
// taken away afterwards and so the same name cannot be asked for twice. See
// release.
func Open(t *testing.T, lock bool) store.Store {
	t.Helper()
	return OpenRaw(t, lock).Store
}

// Raw is a store together with the client and bucket underneath it.
//
// It exists for the tests that have to attempt what store.Store deliberately
// offers no way to do — delete an object — because showing that the bucket
// refuses is the only way to know the archive's central claim is true rather
// than merely unexercised.
type Raw struct {
	Store  store.Store
	Client *s3.Client
	Bucket string
}

// OpenRaw is Open with the client and bucket name kept.
func OpenRaw(t *testing.T, lock bool) Raw {
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
	t.Cleanup(func() { release(client, bucket, lock) })

	mode := s3store.Compliance
	if !lock {
		mode = s3store.None
	}
	built, err := s3store.New(client, s3store.Options{Bucket: bucket, Lock: mode})
	if err != nil {
		t.Fatal(err)
	}
	return Raw{Store: built, Client: client, Bucket: bucket}
}

// release gives back what can be given back.
//
// An unlocked bucket is emptied and removed. A locked one is left exactly where
// it is: its objects are under a retention that refuses deletion until it
// expires, which is the property the archive is built on, and a harness that
// deleted them anyway would be standing in for a bucket nobody would deploy.
// This is why Open's names are unique — the leftovers must not collide with the
// next run, since they cannot be cleared out of its way.
//
// It is best effort either way: a test that leaves a bucket behind is untidy,
// not wrong, and failing the cleanup would hide whatever the test found.
func release(client *s3.Client, bucket string, lock bool) {
	if lock {
		return
	}
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

// suffix is how many random characters end a bucket name.
const suffix = 8

// bucketName turns a test's name into one S3 will take: lower case, letters,
// digits and hyphens, ending in random characters.
//
// The randomness is not decoration. A bucket made with Object Lock outlives the
// test that made it, so a name derived from the test alone is a name that is
// already taken the second time that test runs against the same endpoint — and
// CreateBucket answers BucketAlreadyOwnedByYou, which fails the test for a
// reason that has nothing to do with what it was checking.
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
	// 63 is the longest name S3 takes, and the suffix has to fit inside it.
	out := strings.Trim(b.String(), "-")
	if longest := 63 - suffix - 1; len(out) > longest {
		out = strings.Trim(out[:longest], "-")
	}
	return out + "-" + strings.ToLower(rand.Text()[:suffix])
}

// Fill writes n objects under one profile and tenant on one day, so that a test
// can cross a listing page.
func Fill(t *testing.T, s store.Store, profile, tenant, day string, n int) []string {
	t.Helper()
	ctx := context.Background()
	keys := make([]string, 0, n)
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("profile=%s/tenant=%s/%s/%06d.ndjson.zst", profile, tenant, day, i)
		// The retention is always set, so that Fill serves a locked bucket as
		// well as an unlocked one: a locked archive refuses an object without
		// one, and a harness that only worked on the easy bucket would be the
		// kinder double this package exists to avoid.
		if err := s.Put(ctx, store.Object{
			Key: key, Body: []byte("{}"),
			RetainUntil: time.Now().Add(24 * time.Hour).UTC(),
		}); err != nil {
			t.Fatalf("s3test: %s: %v", key, err)
		}
		keys = append(keys, key)
	}
	return keys
}
