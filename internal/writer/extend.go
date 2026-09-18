package writer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/index"
	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/record"
)

// Addenda: lengthening the lock on evidence already written.
//
// An after_expiry profile keeps a record until the credential it is about has
// expired, plus the profile's years. The writer locks an object for that when
// it writes it, but the credential's life is not always settled then: a renewal
// extends it after issuance, and an identity proofing is relied on by
// credentials issued long after it. A record that says so — its catalogue
// declares the action `extends` the records a data property names — makes the
// writer lengthen the lock on the objects holding those records to the new
// expiry plus the years.
//
// Compliance mode allows lengthening a lock and never shortening it, so this
// is safe to do at any time and undoes nothing; a later, shorter expiry leaves
// the lock as it is. It is done after the addendum is durable, so that the
// extension is never ahead of the record that asked for it, and it never fails
// the batch: the addendum is written either way, and a failure is recorded
// in the trail and counted instead, for an operator to repeat by hand.

// Locator finds the object an earlier record is in. Every index.Searcher is
// one; without an index, a scan finds records within its budget and says so
// when it gives up.
type Locator interface {
	Get(ctx context.Context, profile, id string) (index.Row, index.Provenance, error)
}

// extension is one addendum's request, gathered while the batch is taken and
// acted on once it is durable.
type extension struct {
	record  string
	tenant  string
	profile *preset.Profile
	earlier []string
	until   time.Time
}

// addenda gathers what one record asks to be extended. Only after_expiry
// profiles have anything to extend: under any other, a record's retention does
// not depend on what it is about.
func (w *Writer) addenda(r *record.Record, earlier []string, expiry *time.Time, profiles []string) []extension {
	if len(earlier) == 0 || expiry == nil {
		return nil
	}
	var out []extension
	for _, name := range profiles {
		p := w.Splitter.Profiles[name]
		if p == nil || p.Retention.Policy != "after_expiry" {
			continue
		}
		out = append(out, extension{
			record: r.GetId(), tenant: r.GetTenantId(), profile: p,
			earlier: earlier, until: p.RetainUntil(time.Time{}, expiry),
		})
	}
	return out
}

// extend acts on the addenda of a durable batch.
func (w *Writer) extend(ctx context.Context, pending []extension) {
	for _, e := range pending {
		for _, id := range e.earlier {
			key, previous, err := w.extendOne(ctx, e, id)
			switch {
			case errors.Is(err, errNothingToExtend):
			case err != nil:
				if w.Hooks.OnRetentionNotExtended != nil {
					w.Hooks.OnRetentionNotExtended(e.profile.Name, id, err)
				}
				w.metaExtended(ctx, e, id, key, previous, err)
			default:
				w.metaExtended(ctx, e, id, key, previous, nil)
			}
		}
	}
}

// errNothingToExtend is a lock already as long as the addendum asks. It is no
// fault and is not recorded: nothing changed.
var errNothingToExtend = errors.New("nothing to extend")

func (w *Writer) extendOne(ctx context.Context, e extension, id string) (string, time.Time, error) {
	if w.Records == nil {
		return "", time.Time{}, errors.New(
			"this writer has no way to find earlier records: give it an index, or a scan of the archive")
	}
	row, _, err := w.Records.Get(ctx, e.profile.Name, id)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("finding %s: %w", id, err)
	}
	// An addendum speaks for its own tenant's evidence. Letting one tenant's
	// records hold another's for longer would be a small thing to lengthen and
	// a large thing to have allowed.
	if row.TenantID != e.tenant {
		return row.ObjectKey, time.Time{}, fmt.Errorf("%s belongs to another tenant", id)
	}
	entry, err := w.Roller.Store.Head(ctx, row.ObjectKey)
	if err != nil {
		return row.ObjectKey, time.Time{}, fmt.Errorf("reading the lock on %s: %w", row.ObjectKey, err)
	}
	// A lock with no date is not taken to mean there is none to extend. It is
	// also what a role without s3:GetObjectRetention reads, and skipping then
	// would leave every addendum undone without a word; asking the store to
	// extend it anyway gets an answer — an unlocked store says it has nothing
	// to extend, and that is recorded.
	if !entry.RetainUntil.IsZero() && !entry.RetainUntil.Before(e.until) {
		return row.ObjectKey, entry.RetainUntil, errNothingToExtend
	}
	if err := w.Roller.Store.ExtendRetention(ctx, row.ObjectKey, e.until); err != nil {
		return row.ObjectKey, entry.RetainUntil, err
	}
	return row.ObjectKey, entry.RetainUntil, nil
}

// metaExtended records an extension, or the failure of one. It is recorded
// under the addendum's tenant: it is that tenant's evidence whose keeping
// changed, and that tenant's reader who needs to see why.
func (w *Writer) metaExtended(ctx context.Context, e extension, earlier, key string, previous time.Time, failed error) {
	r := w.meta("audit.retention.extended", auditv1.Operation_OPERATION_MODIFY)
	r.TenantId = e.tenant
	r.Targets = []*record.Target{{Type: "record", Id: earlier}, {Type: "record", Id: e.record}}
	data := map[string]any{"retain_until": e.until.UTC().Format(time.RFC3339)}
	if key != "" {
		data["object"] = key
	}
	if !previous.IsZero() {
		data["previous_retain_until"] = previous.UTC().Format(time.RFC3339)
	}
	if s, err := structpb.NewStruct(data); err == nil {
		r.Data = s
	}
	if failed != nil {
		r.Outcome = &record.Outcome{
			Result: auditv1.Outcome_RESULT_FAILURE,
			Reason: failed.Error(),
			Code:   "not_extended",
		}
	}
	w.emitMeta(ctx, r)
}
