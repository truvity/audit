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
	return &Store{api: api, bucket: o.Bucket, prefix: o.Prefix, kmsKey: o.KMSKeyID, lock: lock}, nil
}

// FromConfig returns a store using the ambient AWS configuration, which in a
// cluster is the workload's own identity.
func FromConfig(cfg aws.Config, o Options) (*Store, error) {
	return New(s3.NewFromConfig(cfg), o)
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
	in := &s3.PutObjectInput{
		Bucket:                    aws.String(s.bucket),
		Key:                       aws.String(s.key(o.Key)),
		Body:                      bytes.NewReader(o.Body),
		ContentLength:             aws.Int64(int64(len(o.Body))),
		ChecksumAlgorithm:         types.ChecksumAlgorithmSha256,
		ObjectLockMode:            s.lock,
		ObjectLockRetainUntilDate: aws.Time(o.RetainUntil.UTC()),
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
	if out.ObjectLockRetainUntilDate != nil {
		e.RetainUntil = out.ObjectLockRetainUntilDate.UTC()
	}
	return e, nil
}

// List implements store.Store.
func (s *Store) List(ctx context.Context, prefix, after string, limit int) ([]store.Entry, error) {
	in := &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(s.key(prefix)),
	}
	if after != "" {
		in.StartAfter = aws.String(s.key(after))
	}
	if limit > 0 {
		in.MaxKeys = aws.Int32(int32(limit))
	}
	out, err := s.api.ListObjectsV2(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("s3store: list %s: %w", prefix, err)
	}
	entries := make([]store.Entry, 0, len(out.Contents))
	for _, o := range out.Contents {
		e := store.Entry{Key: s.unkey(aws.ToString(o.Key)), Size: aws.ToInt64(o.Size)}
		if o.LastModified != nil {
			e.Modified = o.LastModified.UTC()
		}
		entries = append(entries, e)
	}
	return entries, nil
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
