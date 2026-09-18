package s3store_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sort"
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
	holds   []*s3.PutObjectLegalHoldInput
	// pageSize is how many keys one listing answers, for tests about paging.
	pageSize int
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

func (f *fake) PutObjectLegalHold(_ context.Context, in *s3.PutObjectLegalHoldInput, _ ...func(*s3.Options)) (*s3.PutObjectLegalHoldOutput, error) {
	f.holds = append(f.holds, in)
	return &s3.PutObjectLegalHoldOutput{}, nil
}

// ListObjectsV2 behaves as S3 does, not as a test would like: keys come sorted,
// a page holds at most pageSize (a thousand, as S3 has it, unless a test lowers
// it), a truncated answer carries a continuation token, and a delimiter turns
// the level below into common prefixes. A kinder fake is how a listing that
// stopped at one page went unnoticed.
func (f *fake) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	prefix := aws.ToString(in.Prefix)
	delimiter := aws.ToString(in.Delimiter)
	var keys []string
	for key := range f.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	start := aws.ToString(in.StartAfter)
	if token := aws.ToString(in.ContinuationToken); token != "" {
		start = token
	}
	size := f.pageSize
	if size == 0 {
		size = 1000
	}
	if asked := int(aws.ToInt32(in.MaxKeys)); asked > 0 && asked < size {
		size = asked
	}

	out := &s3.ListObjectsV2Output{}
	seen := map[string]bool{}
	count := 0
	for _, key := range keys {
		if key <= start {
			continue
		}
		if count == size {
			out.IsTruncated = aws.Bool(true)
			break
		}
		if delimiter != "" {
			if at := strings.Index(key[len(prefix):], delimiter); at >= 0 {
				group := key[:len(prefix)+at+len(delimiter)]
				if !seen[group] {
					seen[group] = true
					out.CommonPrefixes = append(out.CommonPrefixes, types.CommonPrefix{Prefix: aws.String(group)})
					count++
					out.NextContinuationToken = aws.String(key)
				}
				continue
			}
		}
		out.Contents = append(out.Contents, types.Object{Key: aws.String(key), Size: aws.Int64(int64(len(f.objects[key])))})
		out.NextContinuationToken = aws.String(key)
		count++
	}
	if !aws.ToBool(out.IsTruncated) {
		out.NextContinuationToken = nil
	}
	return out, nil
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
	// An alias rather than an ARN: the test is about the key reaching the
	// request, and a fixture shaped like a real ARN is the shape the leak
	// canary is looking for.
	s, f := newStore(t, s3store.Options{KMSKeyID: "alias/audit-test"})
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

// seed puts keys straight into the fake, as if written long ago.
func seed(f *fake, keys ...string) {
	if f.objects == nil {
		f.objects = map[string][]byte{}
	}
	for _, key := range keys {
		f.objects[key] = []byte("x")
	}
}

// S3 answers a thousand keys at a time whatever is asked. A listing that took
// the first answer for the whole would be right until the archive outgrew it,
// and every job that walks the archive would then be wrong in silence.
func TestListWithoutALimitPagesToTheEnd(t *testing.T) {
	s, f := newStore(t, s3store.Options{})
	f.pageSize = 3
	seed(f, "p/a", "p/b", "p/c", "p/d", "p/e", "p/f", "p/g", "q/zzz")

	entries, err := s.List(context.Background(), "p/", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 7 {
		t.Fatalf("listed %d of 7 keys: a page was taken for the whole", len(entries))
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].Key <= entries[i-1].Key {
			t.Fatalf("out of order at %d: %v", i, entries)
		}
	}
}

// With a limit the caller is paging, and gets one page from after its key.
func TestListWithALimitReturnsOnePageAfterAKey(t *testing.T) {
	s, f := newStore(t, s3store.Options{})
	seed(f, "p/a", "p/b", "p/c", "p/d")

	entries, err := s.List(context.Background(), "p/", "p/b", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Key != "p/c" {
		t.Fatalf("got %v, want the one key after p/b", entries)
	}
}

// The tenants under a profile, without the objects beneath them, and all of
// them however many pages they span.
func TestPrefixesListsEveryGroupAcrossPages(t *testing.T) {
	s, f := newStore(t, s3store.Options{Prefix: "archive"})
	f.pageSize = 2
	seed(f,
		"archive/profile=security/tenant=acme/year=2026/month=09/day=17/a",
		"archive/profile=security/tenant=acme/year=2026/month=09/day=17/b",
		"archive/profile=security/tenant=globex/year=2026/month=09/day=17/a",
		"archive/profile=security/tenant=initech/year=2026/month=09/day=17/a",
		"archive/profile=security/tenant=umbrella/year=2026/month=09/day=17/a",
		"archive/profile=security/tenant=wayne/year=2026/month=09/day=17/a",
		"archive/profile=history/tenant=acme/year=2026/month=09/day=17/a",
	)
	groups, err := s.Prefixes(context.Background(), "profile=security/", "/")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"profile=security/tenant=acme/", "profile=security/tenant=globex/",
		"profile=security/tenant=initech/", "profile=security/tenant=umbrella/",
		"profile=security/tenant=wayne/",
	}
	if len(groups) != len(want) {
		t.Fatalf("got %v, want %v", groups, want)
	}
	for i := range want {
		if groups[i] != want[i] {
			t.Fatalf("got %v, want %v", groups, want)
		}
	}
}

// A hold placed on a prefix covers what is there; an object written afterwards
// carries it from the start, because one held only by a later sweep was
// deletable in between.
func TestPutCarriesTheLegalHold(t *testing.T) {
	s, f := newStore(t, s3store.Options{})
	o := object()
	o.LegalHold = true
	if err := s.Put(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if got := f.puts[0].ObjectLockLegalHoldStatus; got != types.ObjectLockLegalHoldStatusOn {
		t.Fatalf("legal hold status %q, want ON", got)
	}

	plain := object()
	plain.Key += ".2"
	if err := s.Put(context.Background(), plain); err != nil {
		t.Fatal(err)
	}
	if got := f.puts[1].ObjectLockLegalHoldStatus; got != types.ObjectLockLegalHoldStatusOff {
		t.Fatalf("legal hold status %q, want OFF", got)
	}
}

// The sweep sets holds on objects that already exist, under the store's prefix
// like every other key it handles.
func TestSetLegalHoldAddressesThePrefixedKey(t *testing.T) {
	s, f := newStore(t, s3store.Options{Prefix: "archive"})
	if err := s.SetLegalHold(context.Background(), "profile=security/x", true); err != nil {
		t.Fatal(err)
	}
	if len(f.holds) != 1 {
		t.Fatalf("%d calls", len(f.holds))
	}
	if got := aws.ToString(f.holds[0].Key); got != "archive/profile=security/x" {
		t.Fatalf("key %q", got)
	}
	if f.holds[0].LegalHold.Status != types.ObjectLockLegalHoldStatusOn {
		t.Fatalf("status %q", f.holds[0].LegalHold.Status)
	}
}

// The export bucket has no Object Lock, and a put that names a lock mode to
// such a bucket is refused outright. An unlocked store sends none of the three
// headers — and no retention either, because a retention on a file meant to be
// cleared would keep it.
func TestAnUnlockedStoreSendsNoLockHeaders(t *testing.T) {
	s, f := newStore(t, s3store.Options{Unlocked: true})
	o := object()
	o.LegalHold = true
	if err := s.Put(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	put := f.puts[0]
	if put.ObjectLockMode != "" || put.ObjectLockRetainUntilDate != nil || put.ObjectLockLegalHoldStatus != "" {
		t.Fatalf("an unlocked store sent lock headers: mode=%q retain=%v hold=%q",
			put.ObjectLockMode, put.ObjectLockRetainUntilDate, put.ObjectLockLegalHoldStatus)
	}
}
