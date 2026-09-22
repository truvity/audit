package index_test

import (
	"testing"

	"github.com/truvity/audit/index"
	"github.com/truvity/audit/index/indextest"
)

// The memory searcher against the shared corpus.
//
// It is the implementation with nothing underneath it, so a case it fails is a
// case whose expected answer is wrong rather than a case a database got wrong —
// which makes it the right place to notice that the suite itself has a bug.
func TestMemoryConforms(t *testing.T) {
	m := index.NewMemory()
	indextest.Index(t, m, indextest.Corpus(t))
	indextest.Run(t, "memory", m, m)
}
