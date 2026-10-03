package s3test_test

import (
	"sort"
	"testing"

	"github.com/truvity/audit/index"
	"github.com/truvity/audit/internal/observe"
	"github.com/truvity/audit/internal/observe/observetest"
	"github.com/truvity/audit/internal/s3test"
)

// The indexer over a real S3 listing, which pages at a thousand keys, honours
// the delimiter the tenants are discovered by and returns a key in the listing
// as soon as its put has finished. The index and cursors are in memory: what is
// under test is what the archive's listing does to the cursor.
func TestTheIndexerOverS3(t *testing.T) {
	observetest.Run(t, func(t *testing.T) observetest.Env {
		idx := index.NewMemory()
		cursors := &observe.Memory{Index: idx}
		return observetest.Env{
			Store:   s3test.Open(t, false),
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
