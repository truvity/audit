package observe_test

import (
	"sort"
	"testing"

	"github.com/truvity/audit/index"
	"github.com/truvity/audit/internal/observe"
	"github.com/truvity/audit/internal/observe/observetest"
	"github.com/truvity/audit/store/storetest"
)

// The indexer over the in-memory archive and index: every claim of
// docs/decisions/0020, with the clock in the test's hand.
func TestTheIndexerOverMemory(t *testing.T) {
	observetest.Run(t, func(*testing.T) observetest.Env {
		idx := index.NewMemory()
		cursors := &observe.Memory{Index: idx}
		return observetest.Env{
			Store:   storetest.NewMemory(),
			Cursors: func() observe.Cursors { return cursors },
			Indexed: func(profile string) []string {
				var ids []string
				for _, r := range idx.Rows(profile) {
					ids = append(ids, r.ID)
				}
				sort.Strings(ids)
				return ids
			},
		}
	})
}
