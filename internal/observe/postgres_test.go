package observe_test

import (
	"context"
	"testing"

	"github.com/truvity/audit/index/postgres"
	"github.com/truvity/audit/internal/observe"
	"github.com/truvity/audit/internal/observe/observetest"
	"github.com/truvity/audit/internal/pgtest"
	"github.com/truvity/audit/store/storetest"
)

// The same claims over a real Postgres, where the cursor and the rows are one
// transaction and the partitions are made by a function.
func TestTheIndexerOverPostgres(t *testing.T) {
	observetest.Run(t, func(t *testing.T) observetest.Env {
		pool := pgtest.Open(t)
		idx, err := postgres.New(pool)
		if err != nil {
			t.Fatal(err)
		}
		return observetest.Env{
			Store:   storetest.NewMemory(),
			Cursors: func() observe.Cursors { return idx },
			Indexed: func(profile string) []string {
				rows, err := pool.Query(context.Background(),
					`select id::text from events_core where profile = $1 order by id`, profile)
				if err != nil {
					t.Error(err)
					return nil
				}
				defer rows.Close()
				var ids []string
				for rows.Next() {
					var id string
					if err := rows.Scan(&id); err != nil {
						t.Error(err)
					}
					ids = append(ids, id)
				}
				return ids
			},
		}
	})
}
