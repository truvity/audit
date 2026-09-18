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
	"sort"
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
	// Newest first unless the caller says otherwise. Both directions are
	// answered rather than one being accepted and quietly given the other,
	// which is what happened while the walk was the ordering.
	descending := true
	for _, by := range q.Sort {
		switch by.Field {
		case "", index.SortID:
			// The identifier is the tie-break every searcher adds; it does not
			// choose a direction of its own here.
		case index.SortOccurredAt:
			descending = by.Descending
		default:
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
	// always a question about recently — unless the caller asked the other way.
	for _, day := range s.days(from, to, descending) {
		// A cursor is a place in the walk, and the walk is by day. It is not a
		// place in key order: the tenant sits before the date in a key, so a
		// key from an earlier day under a later-sorting tenant compares after
		// the cursor's, and a resume that compared keys alone would skip that
		// tenant's whole day.
		if resume != nil && after(day, resume.day, descending) {
			continue
		}
		keys, err := s.objectsOf(ctx, q.Profile, day)
		if err != nil {
			return index.Page{}, err
		}
		if len(keys) == 0 {
			continue
		}
		// The budget is spent a whole day at a time, because a day is the
		// smallest unit this searcher can order. Stopping in the middle of one
		// would mean emitting rows before reading the records that sort ahead
		// of them, and a page that is not in the order it claims is worse than
		// a page that stops early: a caller pages through it and silently loses
		// records. So a day is read in full or not begun.
		if spent > 0 && (spent+len(keys) > s.budget().Objects || time.Now().After(deadline)) {
			page.More = true
			page.Next = &index.Boundary{Values: []string{dayCursor(day)}}
			return page, nil
		}
		if spent == 0 && len(keys) > s.budget().Objects {
			return index.Page{}, fmt.Errorf(
				"s3scan: %s holds %d objects and this scan's budget is %d: a day is the smallest "+
					"span this searcher can put in order, so it cannot answer without reading one "+
					"whole. Raise the budget, narrow the query, or run the index",
				day.Format("2006-01-02"), len(keys), s.budget().Objects)
		}
		spent += len(keys)

		var rows []index.Row
		for _, key := range keys {
			found, err := s.rowsOf(ctx, decoder, key, q)
			if err != nil {
				return index.Page{}, err
			}
			rows = append(rows, found...)
		}
		// Within the day, the order the caller asked for. The archive is laid
		// out by day and by key, and a key says nothing about when inside the
		// day its records happened, so this sort is the difference between
		// answering the question and answering a neighbouring one.
		sortRows(rows, descending)

		for _, r := range rows {
			if resume != nil && day.Equal(resume.day) && !beyond(r, resume, descending) {
				continue
			}
			page.Rows = append(page.Rows, r)
			if len(page.Rows) > limit {
				page.Rows = page.Rows[:limit]
				page.More = true
				page.Next = cursorOf(page.Rows[len(page.Rows)-1])
				return page, nil
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

// days is the walk, in the order the caller asked for.
func (s *Scanner) days(from, to time.Time, descending bool) []time.Time {
	var out []time.Time
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		out = append(out, day)
	}
	if descending {
		for a, z := 0, len(out)-1; a < z; a, z = a+1, z-1 {
			out[a], out[z] = out[z], out[a]
		}
	}
	return out
}

// sortRows puts one day's rows in the order a query asks for: by when they
// happened, and by identifier where two happened at the same instant, which is
// the same total order the indexed searchers use.
func sortRows(rows []index.Row, descending bool) {
	sort.SliceStable(rows, func(a, b int) bool {
		x, y := rows[a].OccurredAt, rows[b].OccurredAt
		if !x.Equal(y) {
			if descending {
				return x.After(y)
			}
			return x.Before(y)
		}
		return rows[a].ID < rows[b].ID
	})
}

// after says whether a day is one the walk has already passed.
func after(day, mark time.Time, descending bool) bool {
	if descending {
		return day.After(mark)
	}
	return day.Before(mark)
}

// beyond says whether a row sits past the cursor in the order being walked.
func beyond(r index.Row, at *cursor, descending bool) bool {
	if at.occurred.IsZero() {
		// A cursor left by an exhausted budget names a day and nothing within
		// it, because nothing in it was read.
		return true
	}
	if !r.OccurredAt.Equal(at.occurred) {
		if descending {
			return r.OccurredAt.Before(at.occurred)
		}
		return r.OccurredAt.After(at.occurred)
	}
	return r.ID > at.id
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
			// Not found as far as a bounded scan can see. Still not found:
			// retrying the same scan finds nothing more, and the message says
			// why an index would answer differently.
			return index.Row{}, index.Provenance{}, fmt.Errorf(
				"s3scan: %w: %s was not found within this scan's budget; it may be older than the "+
					"horizon, and an index would answer this directly", index.ErrNotFound, id)
		}
		return index.Row{}, index.Provenance{}, fmt.Errorf("s3scan: %w: %s in profile %s", index.ErrNotFound, id, profile)
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

// cursor is where a scan stopped.
//
// It is a day plus a position inside that day's ordering, not a place in the
// archive. It used to be an object and a line, which is what the walk does, and
// that stopped being a resumable position the moment a day was sorted before
// being emitted: the next row in sort order is very often in an object the walk
// has already passed. A cursor belongs to the searcher that issued it as well
// as to the query.
type cursor struct {
	day time.Time
	// occurred and id are the last row handed out. A zero occurred means the
	// budget ran out before the day was read, so the day is owed in full.
	occurred time.Time
	id       string
}

// cursorOf marks the last row of a page. The day comes from the row's own
// occurrence, which is the day its object is keyed under.
func cursorOf(r index.Row) *index.Boundary {
	return &index.Boundary{
		Values: []string{strings.Join([]string{
			r.OccurredAt.UTC().Truncate(24 * time.Hour).Format(dayLayout),
			r.OccurredAt.UTC().Format(time.RFC3339Nano),
			r.ID,
		}, "|")},
		ID:         r.ID,
		RecordedAt: r.RecordedAt,
		Sequence:   r.Sequence,
	}
}

// dayCursor marks a day the budget did not reach.
func dayCursor(day time.Time) string {
	return day.UTC().Format(dayLayout) + "||"
}

// dayLayout is how a cursor writes a day.
const dayLayout = "2006-01-02"

func parseCursor(at *index.Boundary) (*cursor, error) {
	if at == nil || len(at.Values) == 0 {
		return nil, nil
	}
	raw := at.Values[0]
	parts := strings.Split(raw, "|")
	malformed := fmt.Errorf("s3scan: this cursor did not come from this searcher: %q", raw)
	if len(parts) != 3 {
		return nil, malformed
	}
	day, err := time.Parse(dayLayout, parts[0])
	if err != nil {
		return nil, malformed
	}
	out := &cursor{day: day.UTC(), id: parts[2]}
	if parts[1] != "" {
		occurred, err := time.Parse(time.RFC3339Nano, parts[1])
		if err != nil {
			return nil, malformed
		}
		out.occurred = occurred.UTC()
	}
	return out, nil
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
