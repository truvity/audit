package record

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
)

// Record is the canonical audit record. It is the generated protobuf type, so
// a record crosses package and process boundaries without conversion.
type Record = auditv1.Record

type (
	// Actor is who acted, by kind and identifier.
	Actor = auditv1.Actor
	// Capture is the level-gated request and response snapshot.
	Capture = auditv1.Capture
	// Context is where a record came from: address chain, agent, correlation.
	Context = auditv1.Context
	// Meter is the usage measurement of a billable action or a gauge sample.
	Meter = auditv1.Meter
	// Observer is the component that reported a record, stamped by the writer.
	Observer = auditv1.Observer
	// Outcome is how an action ended.
	Outcome = auditv1.Outcome
	// Party is a subject: a kind and an identifier, never a name.
	Party = auditv1.Party
	// Target is what an action was done to.
	Target = auditv1.Target
	// Operation is the coarse action class, one of seven.
	Operation = auditv1.Operation
	// Result is the outcome of an action.
	Result = auditv1.Outcome_Result
	// Level is how much of a request and response a record captured.
	Level = auditv1.Capture_Level
	// MeterKind distinguishes a count from a gauge sample.
	MeterKind = auditv1.Meter_Kind
)

// SchemaVersion is the core schema version this build writes. The major must
// match the package of the proto (audit.v1); the minor rises with every
// additive change. A reader accepts an equal major and a minor no higher than
// its own.
const SchemaVersion = "1.0"

// TenantPlatform is the reserved tenant of records that belong to the
// installation rather than to a customer. It cannot collide with a real tenant
// identifier because "@" is not valid in one.
const TenantPlatform = "@platform"

// NewID mints a record identifier: a UUIDv7, so identifiers sort by time and
// serve as pagination cursors. It is the idempotency key at every hop.
func NewID() string {
	id, err := uuid.NewV7()
	if err != nil {
		// NewV7 fails only if the system entropy source fails, which is not a
		// condition an audit emitter can paper over.
		panic(fmt.Sprintf("audit: cannot mint record id: %v", err))
	}
	return id.String()
}

// Sequencer hands out the per-instance monotonic sequence that lets a reader
// detect gaps in what one producer wrote. It restarts at 1 on every process
// start, which is why a gap check is per (observer.instance, process) and why
// writer lifecycle records exist to mark the boundary.
type Sequencer struct{ n atomic.Uint64 }

// Next returns the next sequence number, starting at 1.
func (s *Sequencer) Next() uint64 { return s.n.Add(1) }

var instanceUnsafe = regexp.MustCompile(`[^a-z0-9-]+`)

// InstanceName is the name this process reports as observer.instance: the pod
// name where there is one, else the hostname, sanitised to what an object key
// may carry.
func InstanceName() string {
	name := os.Getenv("POD_NAME")
	if name == "" {
		name, _ = os.Hostname()
	}
	if name == "" {
		name = "unknown"
	}
	name = instanceUnsafe.ReplaceAllString(strings.ToLower(name), "-")
	name = strings.Trim(name, "-")
	if name == "" {
		name = "unknown"
	}
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

// Assign fills what every record must carry and the emitter has not set: the
// identifier, the time it happened, and the core schema version. It never
// overwrites a value the caller chose, so a replayed or adapted record keeps
// its original identity and time.
func Assign(r *Record) {
	if r.GetId() == "" {
		r.Id = NewID()
	}
	if r.GetOccurredAt() == nil {
		r.OccurredAt = timestamppb.New(time.Now().UTC())
	}
	if r.GetSchemaVersion() == "" {
		r.SchemaVersion = SchemaVersion
	}
	if r.GetOutcome() == nil {
		r.Outcome = &Outcome{Result: auditv1.Outcome_RESULT_SUCCESS}
	} else if r.GetOutcome().GetResult() == auditv1.Outcome_RESULT_UNSPECIFIED {
		r.Outcome.Result = auditv1.Outcome_RESULT_SUCCESS
	}
}

// Stamp records what only the writer may assert: when the record was accepted,
// which component reported it, and the hash of the record as accepted. The
// observer is taken from the publisher's verified identity, never from what the
// caller supplied, so a reporter cannot claim to be another component.
func Stamp(r *Record, observer *Observer, at time.Time) error {
	r.RecordedAt = timestamppb.New(at.UTC())
	r.Observer = observer
	r.Profile = ""
	r.OriginHash = ""
	h, err := OriginHash(r)
	if err != nil {
		return err
	}
	r.OriginHash = h
	return nil
}

// OperationName is the short spelling of an operation, as a catalogue writes it
// and as an index and a query use it. The generated constant is
// OPERATION_CREATE; nothing outside the wire format should have to say that.
func OperationName(op Operation) string {
	return strings.ToLower(strings.TrimPrefix(op.String(), "OPERATION_"))
}

// ResultName is the short spelling of an outcome.
func ResultName(r Result) string {
	return strings.ToLower(strings.TrimPrefix(r.String(), "RESULT_"))
}

// ParseSchemaVersion splits a "major.minor" version.
func ParseSchemaVersion(v string) (major, minor int, err error) {
	if _, err := fmt.Sscanf(v, "%d.%d", &major, &minor); err != nil {
		return 0, 0, fmt.Errorf("audit: schema version %q is not major.minor", v)
	}
	return major, minor, nil
}

// Readable reports whether a reader built for this package's SchemaVersion may
// decode a record written under version v: the major must be equal and the
// minor no higher, which is the compatibility contract of decision 0009.
func Readable(v string) error {
	wantMajor, wantMinor, err := ParseSchemaVersion(SchemaVersion)
	if err != nil {
		return err
	}
	gotMajor, gotMinor, err := ParseSchemaVersion(v)
	if err != nil {
		return err
	}
	switch {
	case gotMajor != wantMajor:
		return fmt.Errorf("audit: record schema %s cannot be read by a %s reader: different major", v, SchemaVersion)
	case gotMinor > wantMinor:
		return fmt.Errorf("audit: record schema %s cannot be read by a %s reader: newer minor", v, SchemaVersion)
	}
	return nil
}
