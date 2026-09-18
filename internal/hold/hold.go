// Package hold places and releases legal holds over the archive.
//
// A legal hold keeps objects undeletable for as long as it is on, whatever
// their retention says, and it has no expiry of its own: it lasts until
// somebody takes it off. That is the point of it — litigation and
// investigations do not run to a schedule a retention policy could have
// anticipated.
//
// Two things follow from a hold being placed on a prefix while the archive
// holds it per object. The objects that already exist have to be swept. And
// objects keep arriving under that prefix, so the writer has to know the hold
// is there and set it as it writes: an object held only by a later sweep was
// deletable in between, which is the window the hold exists to close.
package hold

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/truvity/audit/store"
)

// Prefix is where the record of a hold lives.
//
// In the archive, under the same lock as everything else: a hold that could be
// deleted by whoever placed it would leave no account of what was held or why,
// which is exactly what an investigation later asks.
const Prefix = "holds"

// Record is what was held, by whom, and why.
type Record struct {
	ID      string `json:"id"`
	Profile string `json:"profile"`
	// Tenant narrows a hold to one tenant's copies. Empty holds the profile.
	Tenant string `json:"tenant,omitempty"`
	Reason string `json:"reason"`
	// PlacedBy and ReleasedBy are whoever ran the command, as the deployment
	// identifies them. The archive records the claim; IAM decides whether the
	// call was allowed.
	PlacedBy   string     `json:"placed_by"`
	PlacedAt   time.Time  `json:"placed_at"`
	ReleasedBy string     `json:"released_by,omitempty"`
	ReleasedAt *time.Time `json:"released_at,omitempty"`
	// Objects is how many were swept, which the command reports and the archive
	// does not keep: it is operational detail, and it is not knowable until
	// after the record of intent has to exist.
	Objects int `json:"-"`
}

// Active reports whether the hold is still on.
func (r Record) Active() bool { return r.ReleasedAt == nil }

// Covers reports whether a hold applies to a profile and tenant.
func (r Record) Covers(profile, tenant string) bool {
	if r.Profile != profile {
		return false
	}
	return r.Tenant == "" || r.Tenant == tenant
}

// PlacedKey and ReleasedKey are where a hold's two records live.
//
// Two objects rather than one that changes. Nothing in this archive is
// overwritten — the store refuses it, and under an object lock a second put
// would make a version rather than replace anything — so a hold is recorded the
// way everything else is: append only. What a hold is now is read from which of
// the two exist.
func PlacedKey(id string) string { return Prefix + "/" + id + "/placed.json" }

// ReleasedKey is the second record, written when the hold comes off.
func ReleasedKey(id string) string { return Prefix + "/" + id + "/released.json" }

// prefixOf is the archive prefix a hold covers.
func prefixOf(r Record) string {
	if r.Tenant == "" {
		return "profile=" + r.Profile
	}
	return "profile=" + r.Profile + "/tenant=" + r.Tenant
}

// Store places, releases and lists holds.
type Store struct {
	Store store.Store
	// RetainUntil is how long a hold's own record is locked for. It wants to be
	// the longest retention the deployment has, because the record of a hold
	// has to outlive what it held.
	RetainUntil func(time.Time) time.Time
	Now         func() time.Time
}

// List returns every hold, newest first.
func (s Store) List(ctx context.Context) ([]Record, error) {
	entries, err := s.Store.List(ctx, Prefix+"/", "", 0)
	if err != nil {
		return nil, fmt.Errorf("hold: listing: %w", err)
	}
	byID := map[string]Record{}
	for _, e := range entries {
		var r Record
		body, err := s.Store.Get(ctx, e.Key)
		if err != nil {
			return nil, fmt.Errorf("hold: %s: %w", e.Key, err)
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, fmt.Errorf("hold: %s: %w", e.Key, err)
		}
		// The release names the same hold and carries the release fields; the
		// placement carries everything else. Whichever arrives second fills in
		// what the first did not have.
		if existing, ok := byID[r.ID]; ok {
			r = merge(existing, r)
		}
		byID[r.ID] = r
	}
	out := make([]Record, 0, len(byID))
	for _, r := range byID {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PlacedAt.After(out[j].PlacedAt) })
	return out, nil
}

// merge folds a hold's two records into one.
func merge(a, b Record) Record {
	if b.ReleasedAt == nil {
		a, b = b, a
	}
	// b is the release; a is the placement.
	a.ReleasedAt, a.ReleasedBy = b.ReleasedAt, b.ReleasedBy
	return a
}

// Active returns the holds still on.
//
// The writer asks this once a minute for as long as it runs, so it reads only
// what it has to. The listing alone says which holds have a release record,
// and those are not opened; a hold that was placed and released years ago
// costs one key in a listing, not a fetch every minute until the archive is
// gone.
func (s Store) Active(ctx context.Context) ([]Record, error) {
	entries, err := s.Store.List(ctx, Prefix+"/", "", 0)
	if err != nil {
		return nil, fmt.Errorf("hold: listing: %w", err)
	}
	placed, released := map[string]string{}, map[string]bool{}
	for _, e := range entries {
		id, kind := split(e.Key)
		switch kind {
		case "placed.json":
			placed[id] = e.Key
		case "released.json":
			released[id] = true
		}
	}
	ids := make([]string, 0, len(placed))
	for id := range placed {
		if !released[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	out := make([]Record, 0, len(ids))
	for _, id := range ids {
		body, err := s.Store.Get(ctx, placed[id])
		if err != nil {
			return nil, fmt.Errorf("hold: %s: %w", id, err)
		}
		var r Record
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, fmt.Errorf("hold: %s: %w", id, err)
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PlacedAt.After(out[j].PlacedAt) })
	return out, nil
}

// split reads the hold id and the record kind out of a key under the prefix.
func split(key string) (id, kind string) {
	rest := strings.TrimPrefix(key, Prefix+"/")
	at := strings.LastIndex(rest, "/")
	if at < 0 {
		return "", ""
	}
	return rest[:at], rest[at+1:]
}

// Place puts a hold on everything under a profile, or a tenant within it, and
// records that it did.
//
// The record is written before the sweep. A sweep that failed halfway with no
// record would leave objects held and nothing saying why, and an unexplained
// hold is worse than none: nobody can tell whether releasing it is safe. The
// count of what was swept is reported to the operator and not kept, because it
// is not knowable until after the record has to exist.
func (s Store) Place(ctx context.Context, r Record) (Record, error) {
	switch {
	case r.Profile == "":
		return r, errors.New("hold: name the profile to hold")
	case strings.TrimSpace(r.Reason) == "":
		return r, errors.New("hold: give a reason: a hold nobody can account for cannot be safely released")
	case r.ID == "":
		return r, errors.New("hold: an identifier is required")
	case strings.TrimSpace(r.PlacedBy) == "":
		return r, errors.New("hold: say who is placing it: a hold is an operator's action, and the record has to name the operator")
	}
	if _, err := s.Get(ctx, r.ID); err == nil {
		return r, fmt.Errorf("hold: %s already exists", r.ID)
	}
	r.PlacedAt = s.now()
	if err := s.write(ctx, PlacedKey(r.ID), r); err != nil {
		return r, err
	}

	held, err := s.sweep(ctx, prefixOf(r), true)
	r.Objects = held
	if err != nil {
		return r, err
	}
	return r, nil
}

// Release takes a hold off and records that it came off.
//
// Whether the caller may is not this code's decision: the archive's policy and
// the deployment's break-glass role decide, and a refusal comes back from the
// store. What this guarantees is that the attempt leaves a trace either way.
func (s Store) Release(ctx context.Context, id, by string) (Record, error) {
	if strings.TrimSpace(by) == "" {
		return Record{}, errors.New("hold: say who is releasing it")
	}
	r, err := s.Get(ctx, id)
	if err != nil {
		return r, err
	}
	if !r.Active() {
		return r, fmt.Errorf("hold: %s was already released at %s", id, r.ReleasedAt.Format(time.RFC3339))
	}
	// The release is recorded first. A sweep that clears the hold and then
	// fails to say so leaves objects deletable with the archive still claiming
	// they are held, which is the more dangerous of the two orders.
	at := s.now()
	r.ReleasedAt, r.ReleasedBy = &at, by
	if err := s.write(ctx, ReleasedKey(id), r); err != nil {
		return r, err
	}
	cleared, err := s.sweep(ctx, prefixOf(r), false)
	r.Objects = cleared
	if err != nil {
		return r, err
	}
	return r, nil
}

// Get reads one hold, as it now stands.
func (s Store) Get(ctx context.Context, id string) (Record, error) {
	body, err := s.Store.Get(ctx, PlacedKey(id))
	if err != nil {
		return Record{}, fmt.Errorf("hold: %s: %w", id, err)
	}
	var r Record
	if err := json.Unmarshal(body, &r); err != nil {
		return Record{}, fmt.Errorf("hold: %s: %w", id, err)
	}
	released, err := s.Store.Get(ctx, ReleasedKey(id))
	if err != nil {
		// Not released, or not readable; either way the hold stands as placed.
		return r, nil
	}
	var off Record
	if err := json.Unmarshal(released, &off); err != nil {
		return r, fmt.Errorf("hold: %s: %w", id, err)
	}
	r.ReleasedAt, r.ReleasedBy = off.ReleasedAt, off.ReleasedBy
	return r, nil
}

// sweep sets or clears the hold on everything under a prefix.
func (s Store) sweep(ctx context.Context, prefix string, on bool) (int, error) {
	tenants := []string{prefix + "/"}
	if !strings.Contains(prefix, "/tenant=") {
		found, err := s.Store.Prefixes(ctx, prefix+"/", "/")
		if err != nil {
			return 0, fmt.Errorf("hold: %w", err)
		}
		tenants = found
	}

	count := 0
	for _, tenant := range tenants {
		after := ""
		for {
			entries, err := s.Store.List(ctx, tenant, after, 1000)
			if err != nil {
				return count, fmt.Errorf("hold: %w", err)
			}
			if len(entries) == 0 {
				break
			}
			for _, e := range entries {
				after = e.Key
				if err := s.Store.SetLegalHold(ctx, e.Key, on); err != nil {
					return count, err
				}
				count++
			}
			if len(entries) < 1000 {
				break
			}
		}
	}
	return count, nil
}

func (s Store) write(ctx context.Context, key string, r Record) error {
	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("hold: %w", err)
	}
	retain := r.PlacedAt.AddDate(10, 0, 0)
	if s.RetainUntil != nil {
		retain = s.RetainUntil(r.PlacedAt)
	}
	if err := s.Store.Put(ctx, store.Object{
		Key: key, Body: body, RetainUntil: retain,
		ContentType: "application/json",
		Metadata:    map[string]string{"audit-hold": r.ID, "audit-profile": r.Profile},
	}); err != nil {
		return fmt.Errorf("hold: recording %s: %w", r.ID, err)
	}
	return nil
}

func (s Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
