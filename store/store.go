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
	// LegalHold keeps an object undeletable for as long as it is set,
	// independently of RetainUntil and without any expiry of its own.
	//
	// It is set at write time rather than afterwards because a hold is placed
	// on a prefix and objects keep arriving under it: an object written into a
	// held prefix and only held by a later sweep is an object that was
	// deletable in between, which is the window a hold exists to close.
	LegalHold bool
}

// Entry is one object as a listing sees it.
type Entry struct {
	Key         string
	Size        int64
	Modified    time.Time
	RetainUntil time.Time
	// LegalHold is whether a hold is on the object. A listing does not carry
	// it — S3 does not report it per key — so it is set by Head and left false
	// by List.
	LegalHold bool
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
	// SetLegalHold places or releases a hold on an object that already exists.
	// Placing a hold covers what an archive already holds; releasing one is
	// governed outside this interface, by whatever the deployment's break-glass
	// role is, and this only makes the call.
	SetLegalHold(ctx context.Context, key string, on bool) error
	// Prefixes returns the distinct groups one level under a prefix, as S3's
	// common prefixes: listing "profile=security/" with "/" gives the tenants
	// without walking the objects beneath them.
	//
	// The archive puts the tenant between the profile and the date, so a job
	// that works a day at a time — the digest chain does — cannot build its
	// prefix without first knowing which tenants exist. Walking every object to
	// find out would cost the whole profile once an hour.
	Prefixes(ctx context.Context, prefix, delimiter string) ([]string, error)
}

// Presigner is a store that can hand out a URL to one object for a while.
//
// It is separate from Store because most of what this repository does with an
// archive must not be reachable by a URL anybody can hold: the records are read
// through a service that checks a grant and records the read. Presigning exists
// for exports, which are a deliberate copy of records to somewhere a person can
// download them, and a store that cannot do it is not a lesser store.
type Presigner interface {
	// Presign returns a URL to an object, valid for the given time.
	Presign(ctx context.Context, key string, valid time.Duration) (string, error)
}
