// Package storetest holds the store a test writes to.
//
// A real archive is a bucket configured to refuse the things a test needs to
// do: overwrite an object, remove one, change when one appeared. This package
// gives a store that behaves like that bucket for the properties the system
// depends on, and can also be made to misbehave on purpose, so that a test can
// show the digest chain catches what the bucket was relied on to prevent.
package storetest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/truvity/audit/store"
)

// Memory is an in-memory store that behaves as a locked bucket does: a key is
// written once, retention is remembered, and nothing is deleted. Tests that
// pass against it are tests that would pass against the real thing for the
// properties this system depends on.
//
// It lives in this package and not beside the real stores because an
// in-memory archive is a contradiction: an archive that vanishes on restart is
// not one. Importing it from anything but a test should read as wrong in the
// import path itself.
type Memory struct {
	// Gets counts object fetches, for tests about how much an operation reads.
	Gets int
	// FailPut, when set, is returned instead of writing.
	FailPut error
	// Now is the clock that stamps write times, for tests that care when an
	// object appeared.
	Now func() time.Time

	mu      sync.RWMutex
	objects map[string]store.Object
	written map[string]time.Time
}

// NewMemory returns an empty store.
func NewMemory() *Memory {
	return &Memory{objects: map[string]store.Object{}, written: map[string]time.Time{}}
}

// Put implements Store.
func (m *Memory) Put(_ context.Context, o store.Object) error {
	if m.FailPut != nil {
		return m.FailPut
	}
	if o.Key == "" {
		return errors.New("store: an object needs a key")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.objects == nil {
		m.objects, m.written = map[string]store.Object{}, map[string]time.Time{}
	}
	if _, taken := m.objects[o.Key]; taken {
		return fmt.Errorf("%w: %s", store.ErrExists, o.Key)
	}
	body := make([]byte, len(o.Body))
	copy(body, o.Body)
	o.Body = body
	m.objects[o.Key] = o
	m.written[o.Key] = m.now()
	return nil
}

// Get implements Store.
func (m *Memory) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Counted, so a test can say how much reading an operation is allowed to
	// do: a walk that fetches what a listing already answered is the kind of
	// cost that is invisible here and paid every minute in production.
	m.Gets++
	o, ok := m.objects[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", store.ErrNotFound, key)
	}
	body := make([]byte, len(o.Body))
	copy(body, o.Body)
	return body, nil
}

// Head implements Store.
func (m *Memory) Head(_ context.Context, key string) (store.Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	o, ok := m.objects[key]
	if !ok {
		return store.Entry{}, fmt.Errorf("%w: %s", store.ErrNotFound, key)
	}
	return store.Entry{
		Key: key, Size: int64(len(o.Body)),
		Modified: m.written[key], RetainUntil: o.RetainUntil,
		LegalHold: o.LegalHold,
	}, nil
}

// List implements Store.
func (m *Memory) List(_ context.Context, prefix, after string, limit int) ([]store.Entry, error) {
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
	out := make([]store.Entry, 0, len(keys))
	for _, k := range keys {
		out = append(out, store.Entry{
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

func (m *Memory) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

// Replace overwrites an object, Forget removes one, and Backdate changes when
// one appeared.
//
// A bucket configured as this system asks would refuse all three. They exist so
// that a test can do them anyway and show that the digest chain catches what
// the bucket was relied on to prevent — which is the only way to know that the
// chain is worth having and not just another thing that agrees with itself.
func (m *Memory) Replace(key string, body []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objects[key]
	if !ok {
		return
	}
	o.Body = append([]byte(nil), body...)
	m.objects[key] = o
}

// Forget removes an object.
func (m *Memory) Forget(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	delete(m.written, key)
}

// Backdate changes when an object appears to have been written.
func (m *Memory) Backdate(key string, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.objects[key]; ok {
		m.written[key] = at.UTC()
	}
}

// Object returns what was written under a key, for a test to look at.
func (m *Memory) Object(key string) (store.Object, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	o, ok := m.objects[key]
	return o, ok
}

// Prefixes implements store.Store.
func (m *Memory) Prefixes(_ context.Context, prefix, delimiter string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	found := map[string]bool{}
	for key := range m.objects {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		rest := key[len(prefix):]
		at := strings.Index(rest, delimiter)
		if at < 0 {
			// A key with nothing below it is an object, not a group.
			continue
		}
		found[prefix+rest[:at+len(delimiter)]] = true
	}
	out := make([]string, 0, len(found))
	for p := range found {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// SetLegalHold implements store.Store.
func (m *Memory) SetLegalHold(_ context.Context, key string, on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objects[key]
	if !ok {
		return fmt.Errorf("%w: %s", store.ErrNotFound, key)
	}
	o.LegalHold = on
	m.objects[key] = o
	return nil
}

// ExtendRetention implements store.Store, refusing a shorter date as a bucket
// in compliance mode does. A memory store that let a test shorten a retention
// would be standing in for a bucket nobody could deploy.
func (m *Memory) ExtendRetention(_ context.Context, key string, until time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objects[key]
	if !ok {
		return fmt.Errorf("%w: %s", store.ErrNotFound, key)
	}
	if until.Before(o.RetainUntil) {
		return fmt.Errorf("storetest: %s is retained until %s; compliance mode does not shorten it to %s",
			key, o.RetainUntil.Format(time.RFC3339), until.Format(time.RFC3339))
	}
	o.RetainUntil = until.UTC()
	m.objects[key] = o
	return nil
}

// HeldKeys is every object under a hold, for tests.
func (m *Memory) HeldKeys() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []string
	for key, o := range m.objects {
		if o.LegalHold {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}
