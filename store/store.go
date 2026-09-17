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
	"fmt"
	"sort"
	"strings"
	"sync"
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

// Memory is an in-memory store that behaves as a locked bucket does: a key is
// written once, retention is remembered, and nothing is deleted. Tests that
// pass against it are tests that would pass against the real thing for the
// properties this system depends on.
type Memory struct {
	// FailPut, when set, is returned instead of writing.
	FailPut error

	mu      sync.RWMutex
	objects map[string]Object
	written map[string]time.Time
}

// NewMemory returns an empty store.
func NewMemory() *Memory {
	return &Memory{objects: map[string]Object{}, written: map[string]time.Time{}}
}

// Put implements Store.
func (m *Memory) Put(_ context.Context, o Object) error {
	if m.FailPut != nil {
		return m.FailPut
	}
	if o.Key == "" {
		return errors.New("store: an object needs a key")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.objects == nil {
		m.objects, m.written = map[string]Object{}, map[string]time.Time{}
	}
	if _, taken := m.objects[o.Key]; taken {
		return fmt.Errorf("%w: %s", ErrExists, o.Key)
	}
	body := make([]byte, len(o.Body))
	copy(body, o.Body)
	o.Body = body
	m.objects[o.Key] = o
	m.written[o.Key] = time.Now().UTC()
	return nil
}

// Get implements Store.
func (m *Memory) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	o, ok := m.objects[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	body := make([]byte, len(o.Body))
	copy(body, o.Body)
	return body, nil
}

// Head implements Store.
func (m *Memory) Head(_ context.Context, key string) (Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	o, ok := m.objects[key]
	if !ok {
		return Entry{}, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return Entry{
		Key: key, Size: int64(len(o.Body)),
		Modified: m.written[key], RetainUntil: o.RetainUntil,
	}, nil
}

// List implements Store.
func (m *Memory) List(_ context.Context, prefix, after string, limit int) ([]Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys := make([]string, 0, len(m.objects))
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) && k > after {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
	}
	out := make([]Entry, 0, len(keys))
	for _, k := range keys {
		out = append(out, Entry{
			Key: k, Size: int64(len(m.objects[k].Body)),
			Modified: m.written[k], RetainUntil: m.objects[k].RetainUntil,
		})
	}
	return out, nil
}

// Len is how many objects are held.
func (m *Memory) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.objects)
}

// Keys returns every key, in order.
func (m *Memory) Keys() []string {
	entries, _ := m.List(context.Background(), "", "", 0)
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Key)
	}
	return out
}

// Object returns what was written under a key, for a test to look at.
func (m *Memory) Object(key string) (Object, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	o, ok := m.objects[key]
	return o, ok
}
