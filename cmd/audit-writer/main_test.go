package main

import (
	"testing"
	"time"

	"github.com/truvity/audit/internal/registry"
	"github.com/truvity/audit/record"
)

// The writer's own record of a registration is a record like any other: it
// passes the check every emitter's record passes, and carries the time it
// happened. Built by hand, it once carried none, and the archive keyed it
// under the epoch -- outside every digest window, for the life of its lock.
func TestARegistrationRecordIsAWholeRecord(t *testing.T) {
	r := registrationRecord(registry.Entry{Source: "app", Version: "1.0.0", RegisteredBy: "app"}, "0.2.6")
	if err := record.Check(r, record.Default); err != nil {
		t.Fatalf("the writer's own record fails the check: %v", err)
	}
	if got := r.GetOccurredAt().AsTime(); time.Since(got) > time.Minute || got.IsZero() {
		t.Fatalf("occurred_at = %v, want now", got)
	}
	if r.GetObserver().GetVersion() != "0.2.6" {
		t.Fatalf("observer.version = %q, want the writer's", r.GetObserver().GetVersion())
	}
}
