package index

import (
	"context"
	"time"
)

// Searcher answers questions about what was written.
//
// It is deliberately narrow, and the query it takes is closed: a caller names
// fields and operators from a fixed set rather than handing over an expression.
// That is what lets a grant be AND-ed into every query as one more term, and
// what lets a second implementation of this interface be honest about what it
// can and cannot do.
type Searcher interface {
	// Search returns a page. The cursor in a request must have come from a
	// response to the same query.
	Search(ctx context.Context, q Query) (Page, error)
	// Facets counts values of the named fields under the same filter.
	Facets(ctx context.Context, q Query, fields []string, limit int) ([]Facet, error)
	// Get returns one row and where it came from.
	Get(ctx context.Context, profile, id string) (Row, Provenance, error)
	// Capabilities says what this implementation can do, so that a caller is
	// refused rather than quietly given a narrower answer than it asked for.
	Capabilities() Capabilities
}

// Query is a closed question.
type Query struct {
	Profile string
	// Tenants is the grant: the caller may see these and no others. Empty means
	// every tenant, which only an operator's grant carries.
	Tenants []string
	// Filter is OR-joined: a row matches if it matches any conjunction.
	Filter []Conjunction
	Sort   []SortBy
	Limit  int
	// After is the keyset boundary a cursor carries.
	After *Boundary
	// Backwards pages towards older results.
	Backwards bool
}

// Conjunction is a set of predicates that must all hold.
type Conjunction struct {
	ID            []Predicate
	OccurredAt    []TimePredicate
	RecordedAt    []TimePredicate
	Source        []Predicate
	Action        []Predicate
	Operation     []Predicate
	Outcome       []Predicate
	TenantID      []Predicate
	ActorKind     []Predicate
	ActorID       []Predicate
	SubjectKind   []Predicate
	SubjectID     []Predicate
	RequestID     []Predicate
	TraceID       []Predicate
	ClientAddress []Predicate
	ObserverID    []Predicate
	TargetType    []Predicate
	TargetID      []Predicate
	// Data are predicates on the extension properties a catalogue marked
	// filterable, addressed by JSON pointer.
	Data []PathPredicate
}

// Op is a comparison.
type Op string

// The operators a predicate may use. There is no regular expression and no
// substring search: both are unbounded work on a table that only grows, and an
// index that cannot answer them quickly would answer them slowly instead.
const (
	Equal    Op = "eq"
	NotEqual Op = "ne"
	In       Op = "in"
	NotIn    Op = "nin"
	Prefix   Op = "prefix"
)

// Predicate is one comparison on a text field.
type Predicate struct {
	Op     Op
	Value  string
	Values []string
}

// TimePredicate is a half-open range. Either end may be zero.
type TimePredicate struct {
	From time.Time
	// To is exclusive, so that adjacent ranges neither overlap nor leave a gap.
	To time.Time
}

// PathPredicate is a comparison on an indexed extension property.
type PathPredicate struct {
	Path string
	Op   Op
	// Text, Int and At are the value, whichever kind the property has.
	Text   string
	Values []string
	Int    int64
	At     time.Time
	Kind   Kind
}

// SortBy is one ordering term.
type SortBy struct {
	Field      string
	Descending bool
}

// The fields a page may be ordered by. Every ordering ends with the identifier,
// so that a keyset boundary is always total and a page cannot repeat or skip a
// row whose sort values tie with its neighbour's.
const (
	SortOccurredAt = "occurred_at"
	SortRecordedAt = "recorded_at"
	SortID         = "id"
	SortAction     = "action"
	SortTenantID   = "tenant_id"
	SortSource     = "source"
)

// Boundary is where a page ended: the sort values of its last row, and that
// row's identifier.
//
// Keyset rather than an offset. An offset re-reads everything before it, so a
// deep page costs more than a shallow one, and a row inserted meanwhile shifts
// every page after it — in a trail that is appended to constantly, that means a
// poller silently skipping records.
type Boundary struct {
	Values []string
	ID     string
	// RecordedAt and Sequence are what a tail advances on.
	RecordedAt time.Time
	Sequence   uint64
}

// Page is what a search returns.
type Page struct {
	Rows []Row
	// More says whether the next page would have anything in it. It is answered
	// by asking for one row more than the limit, rather than by counting.
	More bool
	// Next is the boundary for the following page. It is present even on the
	// last page, because a tail keeps polling it and a record recorded after
	// this page must come back through it.
	Next *Boundary
	Prev *Boundary
}

// Facet is one field's counted values.
type Facet struct {
	Field  string
	Values []FacetValue
}

// FacetValue is a value and how many rows carry it.
type FacetValue struct {
	Value string
	Count int64
}

// Provenance says where a row came from, so that an answer can be checked
// against the copy the digest chain accounts for rather than believed.
type Provenance struct {
	ObjectKey string
	Line      int
	// Digest is the key of the digest that accounts for the object, when one
	// does. Empty means no digest covers it yet, which is the ordinary state of
	// the current hour and a finding in any other.
	Digest string
}

// Capabilities is what an implementation can do.
type Capabilities struct {
	// FreeText is whether the searcher can answer an unstructured query.
	FreeText bool
	// Facets is whether it can count values.
	Facets bool
	// DataPredicates is whether it can filter on extension properties.
	DataPredicates bool
	// MaxConjunctions bounds the OR. Beyond it a query is refused rather than
	// turned into work nobody bounded.
	MaxConjunctions int
	// SortFields are the orderings it can answer.
	SortFields []string
}
