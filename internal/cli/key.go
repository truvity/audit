package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/emit"
	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/internal/hold"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
	"github.com/truvity/audit/store"
)

// KeyDestroy erases a tenant's pseudonymisation key for one purpose.
//
// This is what erasure means here. The copies stay — they are under a lock and
// an auditor still needs to see that something happened — but the pseudonyms in
// them can never be recomputed, so nothing in the archive can be tied back to
// the person. It cannot be undone and there is nothing to restore from.
type KeyDestroy struct {
	Provider keys.Provider
	// Store is the archive, read to check for legal holds. It is required:
	// destroying a key whose copies are under hold destroys evidence that may
	// not be destroyed, and the operator should not be the one to remember.
	Store store.Store
	// Sink is where the record of the erasure goes. The catalogue declares this
	// action as block delivery, so it is required and the record is confirmed.
	Sink      sink.Sink
	Catalogue *catalogue.Catalogue

	Tenant  string
	Purpose string
	By      string
	Reason  string
	Out     io.Writer
}

// Run checks the holds, destroys the key, and records that it did.
func (k KeyDestroy) Run(ctx context.Context) error {
	out := k.Out
	if out == nil {
		out = os.Stdout
	}
	switch {
	case k.Provider == nil:
		return errors.New("key destroy: a key provider is required")
	case k.Store == nil:
		return errors.New("key destroy: the archive is required, to check for legal holds")
	case k.Tenant == "" || k.Purpose == "":
		return errors.New("key destroy: name the tenant and the purpose")
	case strings.TrimSpace(k.By) == "":
		return errors.New("key destroy: say who is destroying it: this is irreversible and the record has to name them")
	case k.Sink == nil || k.Catalogue == nil:
		return errors.New(
			"key destroy: a writer is required: the catalogue declares this action as block delivery, " +
				"and an erasure the trail does not record is one nobody can prove happened lawfully")
	}

	if err := k.checkHolds(ctx); err != nil {
		return err
	}

	// The order is deliberate. Recording first would let the trail claim an
	// erasure that then failed, and a reader trusting it would believe a
	// person's data unlinkable when it is not. A missing record is discoverable
	// by comparing the keys that exist to the records of their destruction; a
	// false one is not discoverable at all.
	name := k.Tenant + "/" + k.Purpose
	if err := k.Provider.Destroy(ctx, k.Tenant, keys.Purpose(k.Purpose)); err != nil {
		return fmt.Errorf("key destroy: %s: %w", name, err)
	}

	if err := k.report(ctx); err != nil {
		return fmt.Errorf(
			"key destroy: the key %s IS DESTROYED and the trail does not say so: %w; "+
				"record it by hand before anything else", name, err)
	}
	printf(out, "destroyed %s; its pseudonyms can no longer be recomputed\n", name)
	return nil
}

// checkHolds refuses while a hold covers the tenant's copies.
func (k KeyDestroy) checkHolds(ctx context.Context) error {
	active, err := hold.Store{Store: k.Store}.Active(ctx)
	if err != nil {
		return fmt.Errorf("key destroy: could not read the legal holds: %w", err)
	}
	for _, h := range active {
		if h.Tenant == "" || h.Tenant == k.Tenant {
			return fmt.Errorf(
				"key destroy: hold %s covers profile %s and is still active (%s: %s); "+
					"destroying this key would destroy evidence that may not be destroyed",
				h.ID, h.Profile, h.PlacedBy, h.Reason)
		}
	}
	return nil
}

// report records the erasure, and waits to be told it was taken.
func (k KeyDestroy) report(ctx context.Context) error {
	emitter, err := emit.New(emit.Options{
		Source:    k.Catalogue.Source,
		Catalogue: k.Catalogue,
		Sink:      k.Sink,
		Instance:  record.InstanceName(),
		// Not self-reporting: this is not a job's account of itself but the
		// record of an irreversible act on somebody's data, and the catalogue
		// declares it block for that reason. It is confirmed or it is an error.
	})
	if err != nil {
		return err
	}
	defer emitter.Close() //nolint:errcheck // the record is confirmed or returned as an error

	reason := k.Reason
	if reason == "" {
		reason = "erasure requested"
	}
	return emitter.Record(ctx, &record.Record{
		Action:    "audit.key.destroyed",
		Operation: auditv1.Operation_OPERATION_REMOVE,
		TenantId:  record.TenantPlatform,
		Actor:     &record.Actor{Kind: "operator", Id: k.By},
		Outcome:   &record.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS, Reason: reason},
		Targets: []*record.Target{
			{Type: "key", Id: k.Tenant + "/" + k.Purpose},
			{Type: "tenant", Id: k.Tenant},
		},
	})
}
