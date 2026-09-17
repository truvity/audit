// Package store is the object store the archive lives in.
//
// The interface is small on purpose: the writer puts objects and never
// rewrites one, the reader gets and lists them, and nothing deletes. What makes
// the archive an archive is the store's own configuration — versioning, Object
// Lock in compliance mode, a policy that denies deletes — and not anything this
// code could enforce on its own. What this code does is set a retention on
// every object it writes and never reuse a key.
package store

import (
	"context"
	"errors"
	"time"
)

// Object is one thing written to the archive.
type Object struct {
	Key  string
	Body []byte
	// RetainUntil is when the object may first be deleted. The store is
	// expected to refuse a deletion before it, and to refuse shortening it.
	RetainUntil time.Time
	ContentType string
	// Encoding is the content encoding, "zstd" for a rolled batch.
	Encoding string
	// Metadata is small, and is there so an object can say what it is without
	// being opened.
	Metadata map[string]string
}

// Entry is one object as a listing sees it.
type Entry struct {
	Key         string
	Size        int64
	Modified    time.Time
	RetainUntil time.Time
}

// ErrExists is returned when a key is already taken. Under Object Lock a second
// put would create a version rather than replace anything, so a writer that
// reuses a key is a writer whose objects a digest cannot account for.
var ErrExists = errors.New("store: the key already exists")

// ErrNotFound is returned for a key that was never written.
var ErrNotFound = errors.New("store: no such object")

// Store is an object store.
type Store interface {
	// Put writes an object. It fails with ErrExists rather than overwriting.
	Put(ctx context.Context, o Object) error
	// Get returns an object's body.
	Get(ctx context.Context, key string) ([]byte, error)
	// Head returns an object's listing entry without its body.
	Head(ctx context.Context, key string) (Entry, error)
	// List returns entries under a prefix, in key order, starting after a key.
	List(ctx context.Context, prefix, after string, limit int) ([]Entry, error)
}
