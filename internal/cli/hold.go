package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/truvity/audit/catalogue"
	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/internal/hold"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
	"github.com/truvity/audit/store"
)

// Hold places, releases and lists legal holds.
//
// Placing one is an operator action and releasing one needs the deployment's
// break-glass role; neither is decided here. What this does is make the call
// and record that it was made, so that a hold and its release are as accounted
// for as the records they cover.
type Hold struct {
	Store     store.Store
	Sink      sink.Sink
	Catalogue *catalogue.Catalogue
	// RetainUntil is how long a hold's own record is locked for.
	RetainUntil func(time.Time) time.Time

	Profile, Tenant, Reason, ID, By string
	Now                             func() time.Time
	JSON                            bool
	Out                             io.Writer
}

// Run dispatches place, release or list.
func (h Hold) Run(ctx context.Context, action string) error {
	out := h.Out
	if out == nil {
		out = os.Stdout
	}
	holds := hold.Store{Store: h.Store, RetainUntil: h.RetainUntil, Now: h.Now}

	// Placing and releasing change what may be deleted, and the catalogue
	// declares both block: they are recorded, confirmed, or they do not
	// happen. Refused here, before anything is touched, rather than after a
	// hold has changed state with nothing able to say so. Listing reads only.
	if (action == "place" || action == "release") && (h.Sink == nil || h.Catalogue == nil) {
		return fmt.Errorf(
			"audit hold %s: give --sink: the catalogue declares this action as block delivery, "+
				"and a hold placed or released off the record is one nobody can account for", action)
	}

	switch action {
	case "place":
		return h.place(ctx, out, holds)
	case "release":
		return h.release(ctx, out, holds)
	case "list":
		return h.list(ctx, out, holds)
	default:
		return fmt.Errorf("audit hold: no action %q; place, release or list", action)
	}
}

func (h Hold) place(ctx context.Context, out io.Writer, holds hold.Store) error {
	id := h.ID
	if id == "" {
		// A hold's identifier is how it is released later, so it is worth being
		// readable: the day and the profile, with a unique tail.
		id = fmt.Sprintf("%s-%s-%s", h.now().Format("2006-01-02"), h.Profile, record.NewID()[:8])
	}
	placed, err := holds.Place(ctx, hold.Record{
		ID: id, Profile: h.Profile, Tenant: h.Tenant,
		Reason: h.Reason, PlacedBy: h.by(),
	})
	if err != nil {
		// An attempt that failed part-way may have held some objects already,
		// so it is recorded as what it was: an attempt, with the reason.
		return errors.Join(err, h.record(ctx, "audit.hold.placed", placed, err))
	}
	if err := h.record(ctx, "audit.hold.placed", placed, nil); err != nil {
		return fmt.Errorf(
			"audit hold place: hold %s IS PLACED on %d object(s) and the trail does not say so: %w; "+
				"record it by hand before anything else", placed.ID, placed.Objects, err)
	}
	return h.report(out, placed, fmt.Sprintf("held %d object(s)", placed.Objects))
}

func (h Hold) release(ctx context.Context, out io.Writer, holds hold.Store) error {
	if h.ID == "" {
		return errors.New("audit hold release: name the hold with --id")
	}
	released, err := holds.Release(ctx, h.ID, h.by())
	if err != nil {
		// A release that was refused is recorded as an attempt. Whoever holds
		// the break-glass role should not be able to try quietly — and if even
		// the attempt cannot be recorded, the caller hears about both.
		if released.ID == "" {
			released = hold.Record{ID: h.ID, Profile: h.Profile}
		}
		return errors.Join(err, h.record(ctx, "audit.hold.released", released, err))
	}
	if err := h.record(ctx, "audit.hold.released", released, nil); err != nil {
		return fmt.Errorf(
			"audit hold release: hold %s IS RELEASED from %d object(s), which may now be deleted when "+
				"their retention ends, and the trail does not say so: %w; record it by hand before anything else",
			released.ID, released.Objects, err)
	}
	return h.report(out, released, fmt.Sprintf("released %d object(s)", released.Objects))
}

func (h Hold) list(ctx context.Context, out io.Writer, holds hold.Store) error {
	all, err := holds.List(ctx)
	if err != nil {
		return err
	}
	if h.Profile != "" {
		var kept []hold.Record
		for _, r := range all {
			if r.Covers(h.Profile, h.Tenant) {
				kept = append(kept, r)
			}
		}
		all = kept
	}
	if h.JSON {
		body, err := json.MarshalIndent(all, "", "  ")
		if err != nil {
			return err
		}
		printf(out, "%s\n", body)
		return nil
	}
	if len(all) == 0 {
		printf(out, "no holds\n")
		return nil
	}
	for _, r := range all {
		where := "profile=" + r.Profile
		if r.Tenant != "" {
			where += " tenant=" + r.Tenant
		}
		state := "ACTIVE  "
		if !r.Active() {
			state = "released"
		}
		printf(out, "%s %s  %s  placed %s by %s: %s\n",
			state, r.ID, where, r.PlacedAt.Format(time.RFC3339), r.PlacedBy, r.Reason)
	}
	return nil
}

func (h Hold) report(out io.Writer, r hold.Record, what string) error {
	if h.JSON {
		body, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return err
		}
		printf(out, "%s\n", body)
		return nil
	}
	printf(out, "%s %s: %s\n", r.ID, what, r.Reason)
	return nil
}

// record puts the action in the trail and waits for it to be taken.
//
// It runs after the act rather than before, as key destroy does: recording
// first would let the trail claim a hold that then failed to be placed, and a
// reader relying on it would believe evidence protected that is not. A missing
// record is discoverable — the hold's own record sits in the archive under
// holds/ — and the error says so loudly; a false one would not be.
//
// The actor is the operator, not the tool: a hold is a person's decision, and
// the record has to name who made it.
func (h Hold) record(ctx context.Context, action string, r hold.Record, failure error) error {
	event := &record.Record{
		Action:    action,
		Operation: auditv1.Operation_OPERATION_MODIFY,
		Actor:     &record.Actor{Kind: "operator", Id: h.by()},
		Targets:   []*record.Target{{Type: "hold", Id: r.ID}},
		Outcome:   &record.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS, Reason: r.Reason},
	}
	if failure != nil {
		event.Outcome = &record.Outcome{Result: auditv1.Outcome_RESULT_FAILURE, Reason: failure.Error()}
	}
	return confirm(ctx, h.Catalogue, h.Sink, event)
}

// by is who is acting. It is not defaulted: a hold is an operator's action and
// the record has to name the operator, so the library refuses a blank and the
// refusal says what to pass.
func (h Hold) by() string { return strings.TrimSpace(h.By) }

func (h Hold) now() time.Time {
	if h.Now != nil {
		return h.Now().UTC()
	}
	return time.Now().UTC()
}
