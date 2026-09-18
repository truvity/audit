package s3test_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/truvity/audit/internal/s3test"
	"github.com/truvity/audit/store"
)

// TestALockedObjectSurvivesBeingDeleted is the archive's central claim, asked of
// a bucket rather than of a comment.
//
// Everything else in this repository — the digest chain, the holds, the writer's
// refusal to reuse a key — is built on the belief that once bytes are in the
// archive nobody can take them out. That belief had never been put to a bucket:
// the tamper tests run against a memory store that refuses deletion because it
// was written to refuse deletion, which proves only that the double agrees with
// itself.
//
// Three deletions are attempted here, and the interesting thing is that two of
// them are meant to succeed. S3 does not stop a delete; it stops data being
// lost. A delete without a version leaves a marker over bytes that are still
// there, and a second write makes a new version beside the old one rather than
// on top of it. Only removing the version itself would destroy anything, and
// that is what the retention refuses.
func TestALockedObjectSurvivesBeingDeleted(t *testing.T) {
	raw := s3test.OpenRaw(t, true)
	ctx := context.Background()
	const key = "profile=security/tenant=t/year=2026/month=09/day=18/hour=00/a.ndjson"
	const original = "the record as it was written"

	if err := raw.Store.Put(ctx, store.Object{
		Key: key, Body: []byte(original), RetainUntil: retention(),
	}); err != nil {
		t.Fatal(err)
	}

	// A delete with no version: accepted, and it hides the object.
	if _, err := raw.Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(raw.Bucket), Key: aws.String(key),
	}); err != nil {
		t.Fatalf("a delete marker should be allowed, S3 refused it: %v", err)
	}
	if _, err := raw.Store.Get(ctx, key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after a delete marker the object should read as gone, got %v", err)
	}

	// The bytes are still there, under their version. This is what an auditor
	// with the version id, or a bucket with the marker removed, still has.
	versions := listVersions(t, raw)
	if len(versions) != 1 {
		t.Fatalf("want the one version still held, got %d", len(versions))
	}
	body := getVersion(t, raw, key, versions[0])
	if body != original {
		t.Fatalf("the held version is not what was written: %q", body)
	}

	// And it cannot be removed, which is the whole point.
	_, err := raw.Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(raw.Bucket), Key: aws.String(key), VersionId: aws.String(versions[0]),
	})
	if err == nil {
		t.Fatal("the bucket let a locked version be deleted; the archive is not an archive")
	}
	if !strings.Contains(err.Error(), "AccessDenied") {
		t.Fatalf("want the delete refused as AccessDenied, got %v", err)
	}
}

// TestAnOverwriteCannotReachTheOriginal is the other half: a writer that puts
// the same key twice does not replace what was there.
//
// The store refuses this itself — a key is written once — so the attempt is made
// with the raw client, which is what an operator with the credentials would
// have. Versioning is what saves the original, and versioning is not optional:
// a bucket cannot have Object Lock without it.
func TestAnOverwriteCannotReachTheOriginal(t *testing.T) {
	raw := s3test.OpenRaw(t, true)
	ctx := context.Background()
	const key = "profile=security/tenant=t/year=2026/month=09/day=18/hour=00/b.ndjson"

	if err := raw.Store.Put(ctx, store.Object{
		Key: key, Body: []byte("what happened"), RetainUntil: retention(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := raw.Store.Put(ctx, store.Object{
		Key: key, Body: []byte("what we would rather say happened"), RetainUntil: retention(),
	}); !errors.Is(err, store.ErrExists) {
		t.Fatalf("the store must refuse a key it has already written, got %v", err)
	}
	// Around the store, as someone with the credentials could.
	if _, err := raw.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(raw.Bucket), Key: aws.String(key),
		Body: strings.NewReader("what we would rather say happened"),
	}); err != nil {
		t.Fatalf("S3 does not refuse a new version, and should not have: %v", err)
	}

	versions := listVersions(t, raw)
	if len(versions) != 2 {
		t.Fatalf("want both versions kept, got %d", len(versions))
	}
	var found bool
	for _, v := range versions {
		if getVersion(t, raw, key, v) == "what happened" {
			found = true
		}
	}
	if !found {
		t.Fatal("the original is no longer readable; an overwrite destroyed the record")
	}
}

func listVersions(t *testing.T, raw s3test.Raw) []string {
	t.Helper()
	out, err := raw.Client.ListObjectVersions(ctx(t), &s3.ListObjectVersionsInput{
		Bucket: aws.String(raw.Bucket),
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(out.Versions))
	for _, v := range out.Versions {
		ids = append(ids, aws.ToString(v.VersionId))
	}
	return ids
}

func getVersion(t *testing.T, raw s3test.Raw, key, version string) string {
	t.Helper()
	out, err := raw.Client.GetObject(ctx(t), &s3.GetObjectInput{
		Bucket: aws.String(raw.Bucket), Key: aws.String(key), VersionId: aws.String(version),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Body.Close() }()
	body, err := io.ReadAll(out.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// retention is far enough out that nothing here expires mid-test, and near
// enough that a bucket left behind is not held for years.
func retention() time.Time { return time.Now().Add(24 * time.Hour).UTC() }

func ctx(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}
