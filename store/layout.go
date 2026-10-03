package store

import (
	"context"
	"strings"
	"time"

	"github.com/truvity/audit/internal/ulid"
)

// The v1 layout, as docs/reference/bucket-contract.md specifies it. The
// archive's keys are built and read here and nowhere else, so that a second
// reading of the grammar cannot disagree with the first.

// RecordsPrefix is the prefix under which every record object lives.
const RecordsPrefix = "records/"

// CataloguePrefix is the prefix of the catalogues, one object per application
// and version.
const CataloguePrefix = "catalogue/"

// MetaFormat, MetaSHA256 and MetaCount are the user-defined metadata every
// record object carries. A reader needs a listing and a HEAD and never a body.
const (
	MetaFormat = "format"
	MetaSHA256 = "sha256"
	MetaCount  = "count"
)

// Format is the value of the format metadata: the layout version.
const Format = "1"

// ProfilePrefix is where one profile's records live.
func ProfilePrefix(profile string) string { return RecordsPrefix + profile + "/" }

// TenantPrefix is where one tenant's records under one profile live.
func TenantPrefix(profile, tenant string) string { return ProfilePrefix(profile) + tenant + "/" }

// HourPrefix is where the objects taken in one hour of ingest time live.
func HourPrefix(profile, tenant string, hour time.Time) string {
	return TenantPrefix(profile, tenant) + hour.UTC().Format("2006/01/02/15") + "/"
}

// RecordKey is the key of an ingest batch: the hour is the hour of the
// ULID's own moment, so that the two cannot disagree.
func RecordKey(profile, tenant string, at time.Time, id string) string {
	return HourPrefix(profile, tenant, at) + id
}

// CatalogueKey is where an application's catalogue at a version lives.
func CatalogueKey(app, version string) string { return CataloguePrefix + app + "/" + version }

// SchemaDir is where the extension schemas of an application's catalogue at a
// version are kept, beside the catalogue and outside the contract: a reader
// that wants only the contract ignores it, and one that reads a catalogue's
// data columns needs it.
func SchemaDir(app, version string) string { return "schema/" + app + "/" + version + "/" }

// KeyComponent reports why a string cannot be a profile, a tenant, an
// application or a version in a key, or "" when it can. Only the slash is
// refused: it is the one character a key's grammar gives a meaning to.
func KeyComponent(s string) string {
	switch {
	case s == "":
		return "is empty"
	case strings.Contains(s, "/"):
		return "contains a slash"
	case s == "." || s == "..":
		return "is a relative path element"
	}
	return ""
}

// RecordObject is a record object's key, taken apart.
type RecordObject struct {
	Profile, Tenant string
	// Hour is the hour of ingest time the key names.
	Hour time.Time
	ULID string
}

// ParseRecordKey reads a key of the grammar
//
//	records/<profile>/<tenant>/<yyyy>/<mm>/<dd>/<hh>/<ULID>
//
// and reports whether it is one. The ULID's own moment must fall in the hour
// the key names, which is what lets a listing order be an ingest order.
func ParseRecordKey(key string) (RecordObject, bool) {
	rest, ok := strings.CutPrefix(key, RecordsPrefix)
	if !ok {
		return RecordObject{}, false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 7 {
		return RecordObject{}, false
	}
	o := RecordObject{Profile: parts[0], Tenant: parts[1], ULID: parts[6]}
	if o.Profile == "" || o.Tenant == "" || !ulid.Valid(o.ULID) {
		return RecordObject{}, false
	}
	var n [4]int
	for i, width := range []int{4, 2, 2, 2} {
		part := parts[2+i]
		if len(part) != width {
			return RecordObject{}, false
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return RecordObject{}, false
			}
			n[i] = n[i]*10 + int(c-'0')
		}
	}
	y, mo, d, h := n[0], n[1], n[2], n[3]
	o.Hour = time.Date(y, time.Month(mo), d, h, 0, 0, 0, time.UTC)
	// A date that does not exist is normalised by time.Date, so it is caught
	// by writing the hour back and comparing.
	if o.Hour.Format("2006/01/02/15") != strings.Join(parts[2:6], "/") {
		return RecordObject{}, false
	}
	at, err := ulid.Time(o.ULID)
	if err != nil || !at.Truncate(time.Hour).Equal(o.Hour) {
		return RecordObject{}, false
	}
	return o, true
}

// Tenants lists the tenants that have records under a profile, without
// walking the objects beneath them.
func Tenants(ctx context.Context, s Store, profile string) ([]string, error) {
	prefix := ProfilePrefix(profile)
	found, err := s.Prefixes(ctx, prefix, "/")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(found))
	for _, p := range found {
		out = append(out, strings.TrimSuffix(strings.TrimPrefix(p, prefix), "/"))
	}
	return out, nil
}

// Profiles lists the profiles that have records in the archive.
func Profiles(ctx context.Context, s Store) ([]string, error) {
	found, err := s.Prefixes(ctx, RecordsPrefix, "/")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(found))
	for _, p := range found {
		out = append(out, strings.TrimSuffix(strings.TrimPrefix(p, RecordsPrefix), "/"))
	}
	return out, nil
}

// pageSize is how many keys one listing asks for.
const pageSize = 1000

// WalkHours visits the record objects of one profile and tenant taken in the
// hours from `from` to `to`, both inclusive, in key order: which, because a
// key begins with its ingest hour and ends with a ULID, is the order the
// batches arrived in.
//
// It is one listing paged to its end, started after the hour before the first
// the range names, and stopped at the first key past the last. The tenant is
// the narrowest prefix a range of hours has in common, so a longer range costs
// pages and not requests per hour. `after`, when it is not empty, resumes a
// walk: only keys sorting after it are visited, which is the cursor a follower
// keeps.
//
// The range is of years 1000 to 8999: keys write the year in four digits, and a
// range outside them would not sort as it reads.
//
// fn is given each entry; returning an error stops the walk.
func WalkHours(
	ctx context.Context, s Store, profile, tenant string, from, to time.Time, after string, fn func(Entry) error,
) error {
	first := from.UTC().Truncate(time.Hour)
	last := to.UTC().Truncate(time.Hour)
	if last.Before(first) {
		return nil
	}
	prefix := TenantPrefix(profile, tenant)
	// Every key of the first hour sorts after the bare hour prefix, so
	// starting after it misses nothing.
	start := HourPrefix(profile, tenant, first)
	if after > start {
		start = after
	}
	// Nothing at or past this is in range: the prefix of the hour after the
	// last, which is smaller than any key in that hour.
	stop := HourPrefix(profile, tenant, last.Add(time.Hour))

	for {
		entries, err := s.List(ctx, prefix, start, pageSize)
		if err != nil {
			return err
		}
		for _, e := range entries {
			start = e.Key
			if e.Key >= stop {
				return nil
			}
			if err := fn(e); err != nil {
				return err
			}
		}
		if len(entries) < pageSize {
			return nil
		}
	}
}

// WalkProfile is WalkHours for every tenant of a profile, tenant by tenant.
// Tenants come in key order, so a walk is repeatable; objects of different
// tenants are not interleaved.
func WalkProfile(
	ctx context.Context, s Store, profile string, from, to time.Time, fn func(tenant string, e Entry) error,
) error {
	tenants, err := Tenants(ctx, s, profile)
	if err != nil {
		return err
	}
	for _, tenant := range tenants {
		if err := WalkHours(ctx, s, profile, tenant, from, to, "", func(e Entry) error {
			return fn(tenant, e)
		}); err != nil {
			return err
		}
	}
	return nil
}
