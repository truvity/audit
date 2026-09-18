package s3scan_test

import (
	"testing"
	"time"

	"github.com/truvity/audit/index/indextest"
	"github.com/truvity/audit/index/s3scan"
	"github.com/truvity/audit/store/storetest"
)

// The scanner against the shared corpus.
//
// This is the searcher the suite was written for. It holds no rows: it opens
// objects, decodes records and derives what the index would have stored, so
// every case it passes is a case where two entirely different routes to an
// answer arrive at the same one. The cases it refuses are the other half of the
// contract — see indextest.Run.
func TestScannerConforms(t *testing.T) {
	archive := storetest.NewMemory()
	indextest.Archive(t, archive, indextest.Corpus(t))
	scanner := &s3scan.Scanner{
		Store:  archive,
		Fields: indextest.FieldsOf,
		// The corpus sits a day or two back, and the clock is pinned so that
		// the scan's horizon covers it however long this test lives.
		Now: func() time.Time { return indextest.Late.Add(6 * time.Hour) },
	}
	indextest.Run(t, "s3scan", scanner)
}
