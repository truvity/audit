// Package s3scan answers queries by reading the archive, with no index at all.
//
// It exists for a deployment too small to run a database, and as the third
// implementation of the Searcher interface — the one that cannot cheat. A
// searcher backed by a table can quietly grow a capability the interface never
// promised; one that must open objects and read them cannot, so it keeps the
// contract honest about what a query is allowed to ask.
//
// What it gives up is stated rather than hidden. It walks days newest first and
// reads every object of each, so a query over a year is a year of reading. That
// is bounded by a budget: a scan stops when it has spent it and hands back a
// cursor, rather than running until something times out and leaving the caller
// with nothing.
package s3scan

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/truvity/audit/index"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/store"
)

// Fields says which extension properties a record's catalogue indexes, the same
// question the writer asks. A scan holds the whole record and could filter on
// anything in it; it asks anyway, so that a query answered here and a query
// answered by the index mean the same thing.
type Fields func(ctx context.Context, r *record.Record) (index.Fields, error)

// Scanner reads the archive.
type Scanner struct {
	Store  store.Store
	Fields Fields
	// Budget bounds one request. Default 500 objects, 10 seconds.
	Budget Budget
	// Horizon is how far back a scan with no time filter will walk before it
	// gives up. Default 90 days. Without it, a query with no range would read
	// the archive to its beginning, which for a seven-year retention is not an
	// answer anybody is waiting for.
	Horizon time.Duration
	Now     func() time.Time
}

// Budget is what one request may spend.
type Budget struct {
	Objects int
	Time    time.Duration
}

// Capabilities implements index.Searcher.
//
// It declares less than the indexed searchers, and that is the point of the
// method: a caller is refused what this cannot do, rather than quietly given a
// narrower answer than it asked for.
func (s *Scanner) Capabilities() index.Capabilities {
	return index.Capabilities{
		FreeText: false,
		// Counting values means reading everything that matches, which is the
		// work this searcher exists to bound. A deployment that wants facets
		// wants the index.
		Facets:          false,
		DataPredicates:  true,
		MaxConjunctions: 4,
		// The archive is laid out by day, so that is the only order it can
		// deliver without reading everything first.
		SortFields: []string{index.SortOccurredAt},
	}
}

// Facets implements index.Searcher by refusing.
func (s *Scanner) Facets(context.Context, index.Query, []string, int) ([]index.Facet, error) {
	return nil, errors.New(
		"s3scan: this searcher does not count values: doing so means reading everything that " +
			"matches, which is the work it exists to bound. Run the index for facets")
}

// Search implements index.Searcher.
func (s *Scanner) Search(ctx context.Context, q index.Query) (index.Page, error) {
	if q.Profile == "" {
		return index.Page{}, errors.New("s3scan: a search names one profile")
	}
	if most := s.Capabilities().MaxConjunctions; len(q.Filter) > most {
		return index.Page{}, fmt.Errorf(
			"s3scan: %d conjunctions, and %d is the most this searcher will take",
			len(q.Filter), most)
	}
	for _, by := range q.Sort {
		if by.Field != "" && by.Field != index.SortOccurredAt {
			return index.Page{}, fmt.Errorf(
				"s3scan: cannot order by %q: the archive is laid out by day, and any other "+
					"order means reading everything before answering", by.Field)
		}
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}

	from, to := s.window(q)
	resume, err := parseCursor(q.After)
	if err != nil {
		return index.Page{}, err
	}

	decoder, err := zstd.NewReader(nil)
	if err != nil {
		return index.Page{}, err
	}
	defer decoder.Close()

	deadline := time.Now().Add(s.budget().Time)
	spent := 0
	page := index.Page{}

	// Newest day first, because a question about an audit trail is nearly
	// always a question about recently.
	for day := to; !day.Before(from); day = day.AddDate(0, 0, -1) {
		keys, err := s.objectsOf(ctx, q.Profile, day)
		if err != nil {
			return index.Page{}, err
		}
		for _, key := range keys {
			if resume != nil && key > resume.key {
				// Already read on an earlier page: the walk is newest first, so
				// a key sorting after the cursor's is one we have passed.
				continue
			}
			if spent >= s.budget().Objects || time.Now().After(deadline) {
				// Out of budget. What has been found is returned with a cursor,
				// rather than nothing after a timeout.
				page.More = true
				page.Next = &index.Boundary{Values: []string{key + "#0"}}
				return page, nil
			}
			spent++

			rows, err := s.rowsOf(ctx, decoder, key, q)
			if err != nil {
				return index.Page{}, err
			}
			for _, r := range rows {
				if resume != nil && key == resume.key && r.Line <= resume.line {
					continue
				}
				page.Rows = append(page.Rows, r)
				if len(page.Rows) > limit {
					page.Rows = page.Rows[:limit]
					page.More = true
					last := page.Rows[len(page.Rows)-1]
					page.Next = cursorOf(last)
					return page, nil
				}
			}
		}
	}
	if n := len(page.Rows); n > 0 {
		page.Next = cursorOf(page.Rows[n-1])
	} else if q.After != nil {
		page.Next = q.After
	}
	return page, nil
}

// Get implements index.Searcher.
//
// Without an index there is nothing to look a record up by, so this is a search
// for one identifier — bounded by the same budget, and honest that it may not
// find a record that is there but older than the horizon.
func (s *Scanner) Get(ctx context.Context, profile, id string) (index.Row, index.Provenance, error) {
	page, err := s.Search(ctx, index.Query{
		Profile: profile,
		Filter:  []index.Conjunction{{ID: []index.Predicate{{Op: index.Equal, Value: id}}}},
		Limit:   1,
	})
	if err != nil {
		return index.Row{}, index.Provenance{}, err
	}
	if len(page.Rows) == 0 {
		if page.More {
			return index.Row{}, index.Provenance{}, fmt.Errorf(
				"s3scan: %s was not found within this scan's budget; it may be older than the "+
					"horizon, and an index would answer this directly", id)
		}
		return index.Row{}, index.Provenance{}, fmt.Errorf("s3scan: no record %s in profile %s", id, profile)
	}
	r := page.Rows[0]
	return r, index.Provenance{ObjectKey: r.ObjectKey, Line: r.Line}, nil
}

// window is the range of days to walk.
func (s *Scanner) window(q index.Query) (from, to time.Time) {
	now := s.now()
	to = now
	from = now.Add(-s.horizon())
	for _, c := range q.Filter {
		for _, w := range c.OccurredAt {
			if !w.From.IsZero() && w.From.Before(from) {
				from = w.From
			}
			if !w.To.IsZero() && w.To.After(to) {
				to = w.To
			}
		}
	}
	return from.UTC().Truncate(24 * time.Hour), to.UTC().Truncate(24 * time.Hour)
}

// objectsOf lists one day's objects across every tenant, newest key last.
func (s *Scanner) objectsOf(ctx context.Context, profile string, day time.Time) ([]string, error) {
	var keys []string
	err := store.WalkDays(ctx, s.Store, "profile="+profile, day, day, func(e store.Entry) error {
		keys = append(keys, e.Key)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("s3scan: %w", err)
	}
	// Within a day, newest first, to match the walk.
	for a, z := 0, len(keys)-1; a < z; a, z = a+1, z-1 {
		keys[a], keys[z] = keys[z], keys[a]
	}
	return keys, nil
}

// rowsOf reads one object and returns the rows of it that match.
func (s *Scanner) rowsOf(
	ctx context.Context, decoder *zstd.Decoder, key string, q index.Query,
) ([]index.Row, error) {
	body, err := s.Store.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("s3scan: %s: %w", key, err)
	}
	plain, err := decoder.DecodeAll(body, nil)
	if err != nil {
		return nil, fmt.Errorf("s3scan: %s: %w", key, err)
	}

	var out []index.Row
	for n, line := range strings.Split(strings.TrimRight(string(plain), "\n"), "\n") {
		if line == "" {
			continue
		}
		var copied record.Record
		if err := record.Unmarshal([]byte(line), &copied); err != nil {
			return nil, fmt.Errorf("s3scan: %s:%d: %w", key, n+1, err)
		}
		var fields index.Fields
		if s.Fields != nil {
			if fields, err = s.Fields(ctx, &copied); err != nil {
				return nil, fmt.Errorf("s3scan: %s:%d: %w", key, n+1, err)
			}
		}
		r := index.RowOf(&copied, index.ObjectAt{Key: key, Line: n + 1}, fields)
		if !granted(r, q.Tenants) || !index.Matches(r, q.Filter) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func granted(r index.Row, tenants []string) bool {
	if len(tenants) == 0 {
		return true
	}
	for _, t := range tenants {
		if r.TenantID == t {
			return true
		}
	}
	return false
}

// cursor is where a scan stopped: an object and a line within it.
//
// The indexed searchers carry sort values; this one carries a place in the
// archive, because that is what it can resume from. A cursor belongs to the
// searcher that issued it as well as to the query.
type cursor struct {
	key  string
	line int
}

func cursorOf(r index.Row) *index.Boundary {
	return &index.Boundary{
		Values:     []string{r.ObjectKey + "#" + strconv.Itoa(r.Line)},
		ID:         r.ID,
		RecordedAt: r.RecordedAt,
		Sequence:   r.Sequence,
	}
}

func parseCursor(at *index.Boundary) (*cursor, error) {
	if at == nil || len(at.Values) == 0 {
		return nil, nil
	}
	raw := at.Values[0]
	hash := strings.LastIndex(raw, "#")
	if hash < 0 {
		return nil, fmt.Errorf(
			"s3scan: this cursor did not come from this searcher: %q", raw)
	}
	line, err := strconv.Atoi(raw[hash+1:])
	if err != nil {
		return nil, fmt.Errorf("s3scan: this cursor did not come from this searcher: %q", raw)
	}
	return &cursor{key: raw[:hash], line: line}, nil
}

func (s *Scanner) budget() Budget {
	b := s.Budget
	if b.Objects <= 0 {
		b.Objects = 500
	}
	if b.Time <= 0 {
		b.Time = 10 * time.Second
	}
	return b
}

func (s *Scanner) horizon() time.Duration {
	if s.Horizon > 0 {
		return s.Horizon
	}
	return 90 * 24 * time.Hour
}

func (s *Scanner) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
