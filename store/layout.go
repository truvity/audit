package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// WalkDays visits every object of a profile keyed under a range of days.
//
// It knows the archive's layout — profile, then tenant, then year, month and
// day — and that is the point of it. The tenant sits between the profile and
// the date, so a job that wants one profile's day cannot ask for it as a prefix.
// Listing the whole profile instead is what the digest builder, the verifier
// and the reindex each did on their own, and each was wrong the same way once
// an archive outgrew one page of a listing. Here the tenants are asked for once
// and each day is one bounded listing, paged to its end.
//
// fn is called in key order within a tenant and day. Returning an error stops
// the walk.
func WalkDays(
	ctx context.Context, s Store, prefix string, from, to time.Time, fn func(Entry) error,
) error {
	tenants, err := s.Prefixes(ctx, strings.TrimSuffix(prefix, "/")+"/", "/")
	if err != nil {
		return err
	}
	first := from.UTC().Truncate(24 * time.Hour)
	last := to.UTC().Truncate(24 * time.Hour)
	for _, tenant := range tenants {
		for day := first; !day.After(last); day = day.AddDate(0, 0, 1) {
			dayPrefix := fmt.Sprintf("%syear=%s/month=%s/day=%s/",
				tenant, day.Format("2006"), day.Format("01"), day.Format("02"))
			after := ""
			for {
				entries, err := s.List(ctx, dayPrefix, after, 1000)
				if err != nil {
					return err
				}
				for _, e := range entries {
					after = e.Key
					if err := fn(e); err != nil {
						return err
					}
				}
				if len(entries) < 1000 {
					break
				}
			}
		}
	}
	return nil
}
