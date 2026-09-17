// Package index is the projection a search reads.
//
// The archive is the record: objects under Object Lock, accounted for by a
// signed digest chain. The index is a derived thing, and the whole design
// depends on keeping that distinction: it may be lost, rebuilt from the
// prefixes, or replaced by a different implementation, and none of that touches
// what the trail says. What it must never do is disagree, which is why the
// rows are derived here rather than in each implementation.
//
// The interfaces are narrow on purpose. Three implementations ship — memory,
// an object-storage scan and Postgres — and an interface two of them cannot
// both satisfy honestly is an interface that has been shaped around one
// database.
package index

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/truvity/audit/record"
)

// Indexer writes the projection.
//
// Index is idempotent by (profile, id), including the facet counts it implies.
// Counting is not a separate call, and could not be: counts are additive, so a
// caller that increments them cannot tell a re-delivered record from a new one.
// Only the implementation holding the transaction can, by counting exactly the
// rows its insert actually created.
type Indexer interface {
	// Index writes rows and the facet counts they imply. A row already there
	// changes nothing, including the counts.
	Index(ctx context.Context, profile string, rows []Row) error
	// Purge forgets what a profile no longer keeps. The archive's objects are
	// under a lock and are not affected; this is the index catching up with a
	// retention that has already expired, and with the narrower schedule the
	// identifying columns are kept on.
	Purge(ctx context.Context, profile string, before time.Time, what Scope) error
}

// Scope is what a purge removes.
type Scope int

const (
	// Identifying is the actor, subject, address and request columns, which a
	// profile keeps for less time than the event itself.
	Identifying Scope = iota
	// Everything is the row and its counts, for a profile whose retention has
	// run out.
	Everything
)

// Row is one copy of one record as the index holds it.
//
// It is the copy, not the original: a security copy and a billing copy of one
// event differ in what they carry, and indexing the original would put back
// what the split took out.
type Row struct {
	ID         string
	TenantID   string
	OccurredAt time.Time
	RecordedAt time.Time
	Sequence   uint64
	Source     string
	Action     string
	Operation  string
	Outcome    string

	TargetTypes []string
	TargetIDs   []string

	// ObjectKey and Line are where the copy is in the archive, which is what
	// lets a reader check any answer the index gives against the object the
	// digest chain accounts for.
	ObjectKey string
	Line      int

	// The identifying columns. They are kept apart because a profile purges
	// them on a shorter schedule than the event, and because a store that
	// separates them can grant a reader one without the other.
	ActorKind     string
	ActorID       string
	SubjectKind   string
	SubjectID     string
	ClientAddress string
	RequestID     string
	TraceID       string
	ObserverID    string

	// Data are the extension properties the action marked filterable, by
	// pointer into its data slot.
	Data []Value
}

// Fields says which extension properties of an action are worth indexing. A
// catalogue's schema answers this; the index takes the answer rather than the
// catalogue, so that an implementation of this interface needs neither.
type Fields struct {
	// Filter are pointers a query may name in a predicate.
	Filter []string
	// Facet are pointers a searcher may count. Every facet is filterable.
	Facet []string
}

// Kind is how a value is stored, which decides what a predicate may do with it.
type Kind string

// The kinds an indexed property may have. A schema's type decides which one a
// property gets, so a predicate over it means the same thing everywhere.
const (
	Text Kind = "text"
	Int  Kind = "int"
	Time Kind = "time"
	Bool Kind = "bool"
)

// Value is one indexed extension property.
type Value struct {
	Path string
	Kind Kind
	Text string
	Int  int64
	At   time.Time
	// Facet marks a value the index counts as well as stores.
	Facet bool
}

// FacetDelta is one increment of the counts table.
type FacetDelta struct {
	TenantID string
	Hour     time.Time
	Field    string
	Value    string
	Count    int64
}

// Core fields every row is counted by. An extension property adds its own,
// named for its pointer, which is what lets an application's own data become a
// facet in the viewer without any code.
const (
	FieldAction     = "action"
	FieldOperation  = "operation"
	FieldOutcome    = "outcome"
	FieldSource     = "source"
	FieldActorKind  = "actor_kind"
	FieldTargetType = "target_type"
	// DataField prefixes an extension property's pointer, so that a data
	// property called action cannot be confused with the record's own.
	DataField = "data:"
)

// RowOf derives the row of one copy, as written.
func RowOf(r *record.Record, at ObjectAt, fields Fields) Row {
	row := Row{
		ID:         r.GetId(),
		TenantID:   r.GetTenantId(),
		OccurredAt: r.GetOccurredAt().AsTime().UTC(),
		RecordedAt: r.GetRecordedAt().AsTime().UTC(),
		Sequence:   r.GetSequence(),
		Source:     r.GetSource(),
		Action:     r.GetAction(),
		Operation:  record.OperationName(r.GetOperation()),
		Outcome:    record.ResultName(r.GetOutcome().GetResult()),
		ObjectKey:  at.Key,
		Line:       at.Line,

		ActorKind:   r.GetActor().GetKind(),
		ActorID:     r.GetActor().GetId(),
		SubjectKind: r.GetSubject().GetKind(),
		SubjectID:   r.GetSubject().GetId(),
		RequestID:   r.GetContext().GetRequestId(),
		TraceID:     r.GetContext().GetTraceId(),
		ObserverID:  r.GetObserver().GetId(),
	}
	// The nearest address is the last of the chain, and it is the one a query
	// means by "where from".
	if chain := r.GetContext().GetClientAddresses(); len(chain) > 0 {
		row.ClientAddress = chain[len(chain)-1]
	}
	for _, t := range r.GetTargets() {
		row.TargetTypes = append(row.TargetTypes, t.GetType())
		row.TargetIDs = append(row.TargetIDs, t.GetId())
	}
	row.Data = valuesOf(r.GetData(), fields)
	return row
}

// ObjectAt is where a copy landed in the archive.
type ObjectAt struct {
	Key  string
	Line int
}

// Facets returns the counts one row implies. Every implementation derives them
// the same way because they derive them here.
func (r Row) Facets() []FacetDelta {
	hour := r.RecordedAt.UTC().Truncate(time.Hour)
	var out []FacetDelta
	add := func(field, value string) {
		if value == "" {
			return
		}
		out = append(out, FacetDelta{
			TenantID: r.TenantID, Hour: hour, Field: field, Value: value, Count: 1,
		})
	}
	add(FieldAction, r.Action)
	add(FieldOperation, r.Operation)
	add(FieldOutcome, r.Outcome)
	add(FieldSource, r.Source)
	add(FieldActorKind, r.ActorKind)
	// A record naming the same target type twice counts once: the facet answers
	// how many records touched a tenant, not how many rows a record had.
	seen := map[string]bool{}
	for _, t := range r.TargetTypes {
		if seen[t] {
			continue
		}
		seen[t] = true
		add(FieldTargetType, t)
	}
	for _, v := range r.Data {
		if v.Facet {
			add(DataField+v.Path, v.String())
		}
	}
	return out
}

// String is the value as a facet counts it.
func (v Value) String() string {
	switch v.Kind {
	case Int:
		return strconv.FormatInt(v.Int, 10)
	case Time:
		return v.At.UTC().Format(time.RFC3339)
	case Bool:
		if v.Int != 0 {
			return "true"
		}
		return "false"
	default:
		return v.Text
	}
}

// valuesOf pulls the indexed properties out of a data slot.
func valuesOf(data *structpb.Struct, fields Fields) []Value {
	if data == nil || len(fields.Filter)+len(fields.Facet) == 0 {
		return nil
	}
	facet := make(map[string]bool, len(fields.Facet))
	for _, p := range fields.Facet {
		facet[p] = true
	}
	wanted := make(map[string]bool, len(fields.Filter)+len(fields.Facet))
	for _, p := range fields.Filter {
		wanted[p] = true
	}
	for _, p := range fields.Facet {
		wanted[p] = true
	}

	out := make([]Value, 0, len(wanted))
	for pointer := range wanted {
		v, ok := at(data, pointer)
		if !ok {
			continue
		}
		value, ok := valueOf(pointer, v)
		if !ok {
			continue
		}
		value.Facet = facet[pointer]
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// at walks a JSON pointer into a struct. Only object steps are followed:
// a property under an array is not something a predicate can name.
func at(data *structpb.Struct, pointer string) (*structpb.Value, bool) {
	steps := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	current := structpb.NewStructValue(data)
	for _, step := range steps {
		s := current.GetStructValue()
		if s == nil {
			return nil, false
		}
		next, ok := s.GetFields()[unescape(step)]
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

// unescape reverses the JSON-pointer escaping of ~ and /.
func unescape(step string) string {
	step = strings.ReplaceAll(step, "~1", "/")
	return strings.ReplaceAll(step, "~0", "~")
}

// valueOf decides how one property is stored. A record carries no floating
// point, so a number is an integer or it is a decimal held as a string, and
// either way nothing is rounded on the way into the index.
func valueOf(pointer string, v *structpb.Value) (Value, bool) {
	switch t := v.GetKind().(type) {
	case *structpb.Value_StringValue:
		if at, err := time.Parse(time.RFC3339, t.StringValue); err == nil {
			return Value{Path: pointer, Kind: Time, At: at.UTC(), Text: t.StringValue}, true
		}
		return Value{Path: pointer, Kind: Text, Text: t.StringValue}, true
	case *structpb.Value_NumberValue:
		n := int64(t.NumberValue)
		if float64(n) != t.NumberValue {
			// Records carry no fractional numbers; one here is a fault
			// upstream, and rounding it into the index would hide it.
			return Value{}, false
		}
		return Value{Path: pointer, Kind: Int, Int: n, Text: strconv.FormatInt(n, 10)}, true
	case *structpb.Value_BoolValue:
		var n int64
		if t.BoolValue {
			n = 1
		}
		return Value{Path: pointer, Kind: Bool, Int: n}, true
	default:
		// An object or an array is not something a predicate names; the
		// extension schema marks the leaves.
		return Value{}, false
	}
}

// Merge adds the deltas of several rows together, so that one batch touches
// each counted value once.
func Merge(deltas []FacetDelta) []FacetDelta {
	type key struct {
		tenant, field, value string
		hour                 time.Time
	}
	totals := map[key]int64{}
	for _, d := range deltas {
		totals[key{d.TenantID, d.Field, d.Value, d.Hour}] += d.Count
	}
	out := make([]FacetDelta, 0, len(totals))
	for k, n := range totals {
		out = append(out, FacetDelta{
			TenantID: k.tenant, Hour: k.hour, Field: k.field, Value: k.value, Count: n,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].less(out[j]) })
	return out
}

func (d FacetDelta) less(o FacetDelta) bool {
	switch {
	case d.TenantID != o.TenantID:
		return d.TenantID < o.TenantID
	case !d.Hour.Equal(o.Hour):
		return d.Hour.Before(o.Hour)
	case d.Field != o.Field:
		return d.Field < o.Field
	default:
		return d.Value < o.Value
	}
}

// String describes a delta, for test failures and for the reindex report.
func (d FacetDelta) String() string {
	return fmt.Sprintf("%s %s %s=%s +%d",
		d.TenantID, d.Hour.Format(time.RFC3339), d.Field, d.Value, d.Count)
}
