package s3store_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/truvity/audit/store"
	"github.com/truvity/audit/store/s3store"
)

// fake records what the store asked of S3, which is what these tests are about:
// the archive's properties are in the request, not in the response.
type fake struct {
	puts    []*s3.PutObjectInput
	putErr  error
	objects map[string][]byte
}

func (f *fake) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if f.putErr != nil {
		return nil, f.putErr
	}
	f.puts = append(f.puts, in)
	if f.objects == nil {
		f.objects = map[string][]byte{}
	}
	body, _ := io.ReadAll(in.Body)
	f.objects[aws.ToString(in.Key)] = body
	return &s3.PutObjectOutput{}, nil
}

func (f *fake) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	body, ok := f.objects[aws.ToString(in.Key)]
	if !ok {
		return nil, &types.NoSuchKey{}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(string(body)))}, nil
}

func (f *fake) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	body, ok := f.objects[aws.ToString(in.Key)]
	if !ok {
		return nil, &types.NotFound{}
	}
	until := time.Date(2033, 1, 1, 0, 0, 0, 0, time.UTC)
	modified := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	return &s3.HeadObjectOutput{
		ContentLength:             aws.Int64(int64(len(body))),
		LastModified:              &modified,
		ObjectLockRetainUntilDate: &until,
	}, nil
}

func (f *fake) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	prefix := aws.ToString(in.Prefix)
	var contents []types.Object
	for key, body := range f.objects {
		if strings.HasPrefix(key, prefix) {
			contents = append(contents, types.Object{Key: aws.String(key), Size: aws.Int64(int64(len(body)))})
		}
	}
	return &s3.ListObjectsV2Output{Contents: contents}, nil
}

func newStore(t *testing.T, o s3store.Options) (*s3store.Store, *fake) {
	t.Helper()
	f := &fake{}
	if o.Bucket == "" {
		o.Bucket = "archive"
	}
	s, err := s3store.New(f, o)
	if err != nil {
		t.Fatal(err)
	}
	return s, f
}

func object() store.Object {
	return store.Object{
		Key:         "profile=security/tenant=acme/year=2026/month=09/day=17/object.ndjson.zst",
		Body:        []byte("a record"),
		RetainUntil: time.Date(2027, 9, 17, 0, 0, 0, 0, time.UTC),
		ContentType: "application/x-ndjson",
		Encoding:    "zstd",
		Metadata:    map[string]string{"audit-profile": "security"},
	}
}

// The retention goes on the object, not on the bucket's default: a default
// applies one period to every profile, and the whole point of the profiles is
// that they differ.
func TestPutSetsRetentionPerObject(t *testing.T) {
	s, f := newStore(t, s3store.Options{})
	if err := s.Put(context.Background(), object()); err != nil {
		t.Fatal(err)
	}
	if len(f.puts) != 1 {
		t.Fatalf("%d puts", len(f.puts))
	}
	in := f.puts[0]
	if in.ObjectLockMode != types.ObjectLockModeCompliance {
		t.Fatalf("lock mode = %q, want compliance", in.ObjectLockMode)
	}
	want := time.Date(2027, 9, 17, 0, 0, 0, 0, time.UTC)
	if got := aws.ToTime(in.ObjectLockRetainUntilDate); !got.Equal(want) {
		t.Fatalf("retain until %s, want %s", got, want)
	}
	if in.ChecksumAlgorithm != types.ChecksumAlgorithmSha256 {
		t.Fatalf("checksum = %q, want sha256", in.ChecksumAlgorithm)
	}
	if aws.ToString(in.ContentEncoding) != "zstd" {
		t.Fatalf("encoding = %q", aws.ToString(in.ContentEncoding))
	}
	if in.Metadata["audit-profile"] != "security" {
		t.Fatalf("metadata = %v", in.Metadata)
	}
}

// Governance mode can be bypassed by anyone holding the permission to bypass
// it, and the console sends that header by default. Compliance is the default
// here, and the weaker mode has to be asked for.
func TestComplianceIsTheDefaultLockMode(t *testing.T) {
	s, f := newStore(t, s3store.Options{Governance: true})
	if err := s.Put(context.Background(), object()); err != nil {
		t.Fatal(err)
	}
	if f.puts[0].ObjectLockMode != types.ObjectLockModeGovernance {
		t.Fatal("governance was asked for and not used")
	}

	s, f = newStore(t, s3store.Options{})
	if err := s.Put(context.Background(), object()); err != nil {
		t.Fatal(err)
	}
	if f.puts[0].ObjectLockMode != types.ObjectLockModeCompliance {
		t.Fatal("compliance must be what a store writes unless asked otherwise")
	}
}

// A second put under Object Lock adds a version rather than replacing anything,
// so a writer that reuses a key writes objects a digest cannot account for. The
// write is conditional so that it fails instead.
func TestPutIsConditionalOnTheKeyBeingFree(t *testing.T) {
	s, f := newStore(t, s3store.Options{})
	if err := s.Put(context.Background(), object()); err != nil {
		t.Fatal(err)
	}
	if got := aws.ToString(f.puts[0].IfNoneMatch); got != "*" {
		t.Fatalf("IfNoneMatch = %q, want *; without it a reused key silently adds a version", got)
	}

	f.putErr = &awshttp.ResponseError{
		ResponseError: &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{Response: &http.Response{StatusCode: 412}},
			Err:      errors.New("At least one of the pre-conditions you specified did not hold"),
		},
	}
	err := s.Put(context.Background(), object())
	if !errors.Is(err, store.ErrExists) {
		t.Fatalf("a taken key must be reported as such, got %v", err)
	}
}

func TestPutEncryptsWithTheGivenKey(t *testing.T) {
	s, f := newStore(t, s3store.Options{KMSKeyID: "arn:aws:kms:eu-central-1:1:key/abc"})
	if err := s.Put(context.Background(), object()); err != nil {
		t.Fatal(err)
	}
	if f.puts[0].ServerSideEncryption != types.ServerSideEncryptionAwsKms {
		t.Fatal("the object was not encrypted with the key it was given")
	}
	if aws.ToString(f.puts[0].SSEKMSKeyId) == "" {
		t.Fatal("no key id was sent")
	}
}

func TestPrefixIsAppliedAndRemoved(t *testing.T) {
	s, f := newStore(t, s3store.Options{Prefix: "audit"})
	o := object()
	if err := s.Put(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if got := aws.ToString(f.puts[0].Key); got != "audit/"+o.Key {
		t.Fatalf("key = %q, want the prefix applied", got)
	}
	entries, err := s.List(context.Background(), "profile=security/", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Key != o.Key {
		t.Fatalf("listing = %+v, want the prefix removed", entries)
	}
}

func TestGetAndHead(t *testing.T) {
	s, _ := newStore(t, s3store.Options{})
	o := object()
	if err := s.Put(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	body, err := s.Get(context.Background(), o.Key)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "a record" {
		t.Fatalf("body = %q", body)
	}
	entry, err := s.Head(context.Background(), o.Key)
	if err != nil {
		t.Fatal(err)
	}
	if entry.RetainUntil.IsZero() {
		t.Fatal("head did not report the retention")
	}

	if _, err := s.Get(context.Background(), "nothing/here"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a missing key must be reported as such, got %v", err)
	}
	if _, err := s.Head(context.Background(), "nothing/here"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a missing key must be reported as such, got %v", err)
	}
}

func TestNewChecksItsArguments(t *testing.T) {
	if _, err := s3store.New(nil, s3store.Options{Bucket: "b"}); err == nil {
		t.Error("want a refusal with no client")
	}
	if _, err := s3store.New(&fake{}, s3store.Options{}); err == nil {
		t.Error("want a refusal with no bucket")
	}
}
