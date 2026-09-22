package postgres_test

import (
	"testing"

	"github.com/truvity/audit/index/indextest"
	"github.com/truvity/audit/index/postgres"
	"github.com/truvity/audit/internal/pgtest"
)

// Postgres against the shared corpus.
//
// This is the searcher a deployment of any size actually runs, and the one with
// the most room to drift: its answers come out of SQL rather than out of the
// Go that derives the rows, so a predicate can be translated into something
// that means almost the same thing and nothing notices. Asking it the same
// questions as the memory searcher and the scan is what makes "almost" show up.
func TestPostgresConforms(t *testing.T) {
	pool := pgtest.Open(t)
	idx, err := postgres.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	indextest.Index(t, idx, indextest.Corpus(t))
	indextest.Run(t, "postgres", idx, idx)
}
