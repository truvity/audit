package writer

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/store"
)

// Roller gathers copies into objects and puts them.
//
// One object holds the copies of one profile, for one tenant, for one day. The
// partition order is profile first and deliberately: an object store's
// lifecycle rules filter by literal prefix and take no wildcards, so a rule
// that moves one profile's objects to colder storage after its hot window can
// only exist if the profile is the leading component. Per-tenant credentials
// are still expressible, because a policy's resource may carry a wildcard where
// a lifecycle filter may not.
type Roller struct {
	// Store is where objects go.
	Store store.Store
	// Instance names this writer in every key it writes, so that two writers
	// cannot collide and a reader can tell their objects apart.
	Instance string
	// Interval rolls an object that has been open this long. Default 5m.
	Interval time.Duration
	// MaxBytes rolls an object that has grown this large, measured before
	// compression. Default 8 MiB.
	MaxBytes int
	// Now is the clock, for tests.
	Now func() time.Time
	// OnPut is called after each object is written.
	OnPut func(key string, records int)

	mu      sync.Mutex
	open    map[partition]*batch
	seq     atomic.Uint64
	encoder *zstd.Encoder
}

type partition struct {
	profile string
	tenant  string
	day     string
}

type batch struct {
	profile  *preset.Profile
	opened   time.Time
	first    time.Time
	bytes    int
	lines    [][]byte
	retainAt time.Time
}

// Add puts one copy into the object being gathered for its profile, tenant and
// day, rolling that object first if it is full or old.
func (r *Roller) Add(ctx context.Context, p *preset.Profile, c *record.Record) error {
	line, err := record.Canonical(c)
	if err != nil {
		return fmt.Errorf("writer: %w", err)
	}
	occurred := c.GetOccurredAt().AsTime().UTC()
	key := partition{profile: p.Name, tenant: tenantOf(c), day: occurred.Format("2006/01/02")}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensure()

	b, ok := r.open[key]
	if ok && r.full(b) {
		if err := r.put(ctx, key, b); err != nil {
			return err
		}
		ok = false
	}
	if !ok {
		b = &batch{
			profile: p,
			opened:  r.now(),
			first:   occurred,
			// The retention is fixed when the object is opened, not when it is
			// written, so every copy in it is kept at least as long as the
			// profile asks of the oldest.
			retainAt: p.RetainUntil(r.now(), nil),
		}
		r.open[key] = b
	}
	b.lines = append(b.lines, line)
	b.bytes += len(line) + 1
	if occurred.Before(b.first) {
		b.first = occurred
	}
	return nil
}

// Flush rolls every open object. It is called on a timer, before the writer
// acknowledges anything it must not lose, and on shutdown.
func (r *Roller) Flush(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensure()

	keys := make([]partition, 0, len(r.open))
	for k := range r.open {
		keys = append(keys, k)
	}
	// A stable order so that a failure leaves the same objects written every
	// time, which is what makes a retry after one predictable.
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].profile != keys[j].profile {
			return keys[i].profile < keys[j].profile
		}
		if keys[i].tenant != keys[j].tenant {
			return keys[i].tenant < keys[j].tenant
		}
		return keys[i].day < keys[j].day
	})
	for _, k := range keys {
		if err := r.put(ctx, k, r.open[k]); err != nil {
			return err
		}
	}
	return nil
}

// Due reports whether anything has been open long enough to roll.
func (r *Roller) Due() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range r.open {
		if r.full(b) {
			return true
		}
	}
	return false
}

// Pending is how many copies are gathered but not yet written.
func (r *Roller) Pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, b := range r.open {
		n += len(b.lines)
	}
	return n
}

// Close releases the compressor.
func (r *Roller) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.encoder != nil {
		return r.encoder.Close()
	}
	return nil
}

// put writes one object and forgets the batch. The caller holds the lock.
func (r *Roller) put(ctx context.Context, key partition, b *batch) error {
	if b == nil || len(b.lines) == 0 {
		delete(r.open, key)
		return nil
	}
	var buf bytes.Buffer
	for _, line := range b.lines {
		buf.Write(line)
		buf.WriteByte('\n')
	}
	r.encoder.Reset(nil)
	body := r.encoder.EncodeAll(buf.Bytes(), nil)

	name := fmt.Sprintf("%019d-%s-%06d.ndjson.zst", b.first.UnixNano(), r.Instance, r.seq.Add(1))
	objectKey := fmt.Sprintf("%s/tenant=%s/year=%s/month=%s/day=%s/%s",
		b.profile.Prefix, key.tenant,
		b.first.Format("2006"), b.first.Format("01"), b.first.Format("02"), name)

	err := r.Store.Put(ctx, store.Object{
		Key:         objectKey,
		Body:        body,
		RetainUntil: b.retainAt,
		ContentType: "application/x-ndjson",
		Encoding:    "zstd",
		Metadata: map[string]string{
			"audit-profile": b.profile.Name,
			"audit-tenant":  key.tenant,
			"audit-records": fmt.Sprint(len(b.lines)),
			"audit-writer":  r.Instance,
		},
	})
	if err != nil {
		// The batch stays open. Losing it here would lose records the writer
		// has already taken responsibility for.
		return fmt.Errorf("writer: put %s: %w", objectKey, err)
	}
	if r.OnPut != nil {
		r.OnPut(objectKey, len(b.lines))
	}
	delete(r.open, key)
	return nil
}

func (r *Roller) ensure() {
	if r.open == nil {
		r.open = map[partition]*batch{}
	}
	if r.encoder == nil {
		e, err := zstd.NewWriter(nil)
		if err != nil {
			// zstd.NewWriter fails only on a bad option, and there are none.
			panic(fmt.Sprintf("writer: zstd: %v", err))
		}
		r.encoder = e
	}
	if r.Instance == "" {
		r.Instance = record.InstanceName()
	}
}

func (r *Roller) full(b *batch) bool {
	interval := r.Interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	limit := r.MaxBytes
	if limit <= 0 {
		limit = 8 << 20
	}
	return b.bytes >= limit || r.now().Sub(b.opened) >= interval
}

func (r *Roller) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func tenantOf(c *record.Record) string {
	if t := c.GetTenantId(); t != "" {
		return t
	}
	// A copy whose profile drops the tenant still has to land somewhere, and
	// the platform partition is where records with no customer belong.
	return record.TenantPlatform
}
