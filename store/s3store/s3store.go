// Package s3store keeps the archive in an S3 bucket.
//
// What makes the archive an archive is the bucket: versioning, Object Lock in
// compliance mode, a policy that denies deletes, encryption with a key the
// writer may use and nobody may destroy. This package sets a retention on every
// object it writes and refuses to reuse a key, and it does nothing else that
// the bucket's own configuration should be doing. See docs/operations/s3-guide.md.
package s3store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/truvity/audit/store"
)

// API is the part of the S3 client this package uses, so a test can stand in
// for it without a network.
type API interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	HeadObject(ctx context.Context, in *s3.HeadObjectInput, opts ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	PutObjectLegalHold(ctx context.Context, in *s3.PutObjectLegalHoldInput, opts ...func(*s3.Options)) (*s3.PutObjectLegalHoldOutput, error)
	PutObjectRetention(ctx context.Context, in *s3.PutObjectRetentionInput, opts ...func(*s3.Options)) (*s3.PutObjectRetentionOutput, error)
}

// Store is an object store backed by a bucket.
type Store struct {
	api    API
	bucket string
	prefix string
	kmsKey string
	// lock is the Object Lock mode written on every object. Compliance is the
	// only mode that means what this system says it means: governance can be
	// bypassed by anyone holding the permission to bypass it, and the console
	// sends that header by default.
	lock types.ObjectLockMode
	// unlocked is the export bucket's shape; see Options.Unlocked.
	unlocked bool
	// presign is set when the store was built from a config, which is what a
	// presigner needs. A store built from a bare API — the tests — has none,
	// and says so rather than returning a URL that would not work.
	presign *s3.PresignClient
}

// Options configure a store.
type Options struct {
	// Bucket the archive lives in.
	Bucket string
	// Prefix under which every key is written. Empty is the bucket root.
	Prefix string
	// KMSKeyID encrypts objects. Empty uses whatever the bucket's default
	// encryption is, which a deployment should still be setting.
	KMSKeyID string
	// Governance writes the weaker lock mode. It exists for a non-production
	// bucket where a mistake has to be undoable, and it is not what a real
	// archive uses.
	Governance bool
	// Unlocked writes no lock at all, and is for the one bucket that must not
	// have one: exports. An export is a copy of records made to be taken away
	// and then cleared, and a lock would keep it instead. A bucket without
	// Object Lock refuses a put that names a lock mode, so this is also the
	// only way to write to such a bucket.
	Unlocked bool
}

// New returns a store.
func New(api API, o Options) (*Store, error) {
	if api == nil {
		return nil, errors.New("s3store: a client is required")
	}
	if o.Bucket == "" {
		return nil, errors.New("s3store: a bucket is required")
	}
	lock := types.ObjectLockModeCompliance
	if o.Governance {
		lock = types.ObjectLockModeGovernance
	}
	return &Store{
		api: api, bucket: o.Bucket, prefix: o.Prefix, kmsKey: o.KMSKeyID,
		lock: lock, unlocked: o.Unlocked,
	}, nil
}

// FromConfig returns a store using the ambient AWS configuration, which in a
// cluster is the workload's own identity.
func FromConfig(cfg aws.Config, o Options) (*Store, error) {
	client := s3.NewFromConfig(cfg)
	built, err := New(client, o)
	if err != nil {
		return nil, err
	}
	// Only a store built this way can presign: it needs the signer the config
	// carries, which a bare API value does not have.
	built.presign = s3.NewPresignClient(client)
	return built, nil
}

// Put implements store.Store.
//
// The retention is set on the object itself rather than left to the bucket's
// default, because a default applies one period to every profile and the whole
// point of the profiles is that they differ. The write is conditional on the
// key being free, so a writer that would reuse one fails instead of adding a
// version the digest cannot account for.
func (s *Store) Put(ctx context.Context, o store.Object) error {
	if o.Key == "" {
		return errors.New("s3store: an object needs a key")
	}
	// A locked bucket takes a mode and a date together or neither, so an object
	// with no retention would be sent as half a lock and refused by S3 with a
	// message about headers. Refusing it here says what is actually wrong, and
	// makes the alternative — dropping the lock headers and writing the object
	// unlocked into the archive — impossible to reach by accident. Nothing may
	// enter the archive without a retention; that is what the archive is.
	if !s.unlocked && o.RetainUntil.IsZero() {
		return fmt.Errorf("s3store: %s: an object in a locked archive must carry a retention", o.Key)
	}
	in := &s3.PutObjectInput{
		Bucket:                    aws.String(s.bucket),
		Key:                       aws.String(s.key(o.Key)),
		Body:                      bytes.NewReader(o.Body),
		ContentLength:             aws.Int64(int64(len(o.Body))),
		ChecksumAlgorithm:         types.ChecksumAlgorithmSha256,
		ObjectLockMode:            s.lockMode(),
		ObjectLockRetainUntilDate: s.retainUntil(o),
		ObjectLockLegalHoldStatus: s.legalHoldStatus(o),
		IfNoneMatch:               aws.String("*"),
		Metadata:                  o.Metadata,
	}
	if o.ContentType != "" {
		in.ContentType = aws.String(o.ContentType)
	}
	if o.Encoding != "" {
		in.ContentEncoding = aws.String(o.Encoding)
	}
	if s.kmsKey != "" {
		in.ServerSideEncryption = types.ServerSideEncryptionAwsKms
		in.SSEKMSKeyId = aws.String(s.kmsKey)
	}
	if _, err := s.api.PutObject(ctx, in); err != nil {
		if taken(err) {
			return fmt.Errorf("%w: %s", store.ErrExists, o.Key)
		}
		return fmt.Errorf("s3store: put %s: %w", o.Key, err)
	}
	return nil
}

// Get implements store.Store.
func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	out, err := s.api.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(s.key(key)),
	})
	if err != nil {
		if missing(err) {
			return nil, fmt.Errorf("%w: %s", store.ErrNotFound, key)
		}
		return nil, fmt.Errorf("s3store: get %s: %w", key, err)
	}
	defer out.Body.Close() //nolint:errcheck // reading is what can fail, and it is reported
	body, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("s3store: get %s: %w", key, err)
	}
	return body, nil
}

// Head implements store.Store.
func (s *Store) Head(ctx context.Context, key string) (store.Entry, error) {
	out, err := s.api.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(s.key(key)),
	})
	if err != nil {
		if missing(err) {
			return store.Entry{}, fmt.Errorf("%w: %s", store.ErrNotFound, key)
		}
		return store.Entry{}, fmt.Errorf("s3store: head %s: %w", key, err)
	}
	e := store.Entry{Key: key, Size: aws.ToInt64(out.ContentLength)}
	if out.LastModified != nil {
		e.Modified = out.LastModified.UTC()
	}
	e.LegalHold = out.ObjectLockLegalHoldStatus == types.ObjectLockLegalHoldStatusOn
	if out.ObjectLockRetainUntilDate != nil {
		e.RetainUntil = out.ObjectLockRetainUntilDate.UTC()
	}
	return e, nil
}

// List implements store.Store.
//
// With a limit it returns one page and the caller continues from the last key.
// Without one it pages to the end itself: S3 answers a thousand keys at a time
// whatever is asked, and a caller that took the first answer for the whole
// would be right until the archive outgrew it and wrong in silence after.
func (s *Store) List(ctx context.Context, prefix, after string, limit int) ([]store.Entry, error) {
	var entries []store.Entry
	var token *string
	for {
		in := &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(s.key(prefix)),
			ContinuationToken: token,
		}
		if after != "" && token == nil {
			in.StartAfter = aws.String(s.key(after))
		}
		if limit > 0 {
			in.MaxKeys = aws.Int32(int32(limit))
		}
		out, err := s.api.ListObjectsV2(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("s3store: list %s: %w", prefix, err)
		}
		for _, o := range out.Contents {
			e := store.Entry{Key: s.unkey(aws.ToString(o.Key)), Size: aws.ToInt64(o.Size)}
			if o.LastModified != nil {
				e.Modified = o.LastModified.UTC()
			}
			entries = append(entries, e)
		}
		if limit > 0 || !aws.ToBool(out.IsTruncated) || aws.ToString(out.NextContinuationToken) == "" {
			return entries, nil
		}
		token = out.NextContinuationToken
	}
}

// SetLegalHold implements store.Store.
func (s *Store) SetLegalHold(ctx context.Context, key string, on bool) error {
	_, err := s.api.PutObjectLegalHold(ctx, &s3.PutObjectLegalHoldInput{
		Bucket:    aws.String(s.bucket),
		Key:       aws.String(s.key(key)),
		LegalHold: &types.ObjectLockLegalHold{Status: legalHold(on)},
	})
	if err != nil {
		return fmt.Errorf("s3store: legal hold on %s: %w", key, err)
	}
	return nil
}

// ExtendRetention implements store.Store. The bucket itself refuses a shorter
// date under compliance mode; this does not try to be cleverer than that.
func (s *Store) ExtendRetention(ctx context.Context, key string, until time.Time) error {
	if s.unlocked {
		return fmt.Errorf("s3store: %s is in an unlocked bucket and has no retention to extend", key)
	}
	_, err := s.api.PutObjectRetention(ctx, &s3.PutObjectRetentionInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.key(key)),
		Retention: &types.ObjectLockRetention{
			Mode:            types.ObjectLockRetentionMode(s.lock),
			RetainUntilDate: aws.Time(until.UTC()),
		},
	})
	if err != nil {
		return fmt.Errorf("s3store: extending the retention of %s: %w", key, err)
	}
	return nil
}

// The three lock headers, or none. An unlocked store sends none: naming a lock
// mode to a bucket without Object Lock is refused outright, and a retention on
// a file meant to be cleared would keep it.
func (s *Store) lockMode() types.ObjectLockMode {
	if s.unlocked {
		return ""
	}
	return s.lock
}

// retainUntil is the date, or nothing for an unlocked store. Put has already
// refused a locked object without one.
func (s *Store) retainUntil(o store.Object) *time.Time {
	if s.unlocked {
		return nil
	}
	return aws.Time(o.RetainUntil.UTC())
}

// legalHoldStatus is sent only to PLACE a hold, never to say there is none.
//
// S3 charges `s3:PutObjectLegalHold` for the header's presence, whatever its
// value, so sending OFF on every put would oblige every component that writes
// the archive to hold the right to place holds -- including the digest and
// verify jobs, which write their own results and have no business placing one.
// An absent header and OFF leave the object in the same state, because a
// bucket has no default legal hold the way it has a default retention.
func (s *Store) legalHoldStatus(o store.Object) types.ObjectLockLegalHoldStatus {
	if s.unlocked || !o.LegalHold {
		return ""
	}
	return legalHold(true)
}

func legalHold(on bool) types.ObjectLockLegalHoldStatus {
	if on {
		return types.ObjectLockLegalHoldStatusOn
	}
	return types.ObjectLockLegalHoldStatusOff
}

// Prefixes implements store.Store.
//
// It pages to the end rather than taking the first response: a deployment with
// more than a thousand tenants that silently lost the rest would produce
// digests covering some of its archive, which is the one failure a digest chain
// must not have.
func (s *Store) Prefixes(ctx context.Context, prefix, delimiter string) ([]string, error) {
	var out []string
	var token *string
	for {
		result, err := s.api.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(s.key(prefix)),
			Delimiter:         aws.String(delimiter),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("s3store: prefixes of %s: %w", prefix, err)
		}
		for _, p := range result.CommonPrefixes {
			out = append(out, s.unkey(aws.ToString(p.Prefix)))
		}
		if !aws.ToBool(result.IsTruncated) || aws.ToString(result.NextContinuationToken) == "" {
			break
		}
		token = result.NextContinuationToken
	}
	sort.Strings(out)
	return out, nil
}

// Presign implements store.Presigner.
//
// The lifetime is the caller's and is meant to be short: a link to audit
// records that outlives the conversation it was shared in is a copy of the
// trail that nobody is tracking.
func (s *Store) Presign(ctx context.Context, key string, valid time.Duration) (string, error) {
	if s.presign == nil {
		return "", errors.New("s3store: this store was built without a presigner")
	}
	out, err := s.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.key(key)),
	}, s3.WithPresignExpires(valid))
	if err != nil {
		return "", fmt.Errorf("s3store: presign %s: %w", key, err)
	}
	return out.URL, nil
}

func (s *Store) key(k string) string {
	if s.prefix == "" {
		return k
	}
	return s.prefix + "/" + k
}

func (s *Store) unkey(k string) string {
	if s.prefix == "" {
		return k
	}
	return trimPrefix(k, s.prefix+"/")
}

func trimPrefix(s, prefix string) string {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return s
}

// taken reports a conditional write that lost, which is what a key already in
// use looks like.
func taken(err error) bool {
	var response *awshttp.ResponseError
	if errors.As(err, &response) {
		return response.HTTPStatusCode() == 412 || response.HTTPStatusCode() == 409
	}
	return false
}

func missing(err error) bool {
	var noKey *types.NoSuchKey
	if errors.As(err, &noKey) {
		return true
	}
	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	var response *awshttp.ResponseError
	if errors.As(err, &response) {
		return response.HTTPStatusCode() == 404
	}
	return false
}
