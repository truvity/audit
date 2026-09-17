package writer

import "fmt"

// GuardReplicas refuses a configuration whose deduplication cannot do its job.
//
// Every hop below the writer is at-least-once, and the writer's deduplication
// is what makes that safe. An in-process table only makes one process's own
// repeats safe: with two replicas behind one stream, a redelivery that lands on
// the other replica is written twice, and a duplicated billing record is an
// invoice nobody can defend.
//
// The failure is silent by nature — two copies of a record look exactly like
// two records — so this refuses at start-up rather than waiting for somebody to
// notice. A deployment that genuinely wants one replica says so by running one.
func GuardReplicas(replicas int, dedupe Dedupe) error {
	if replicas <= 1 {
		return nil
	}
	if _, inProcess := dedupe.(*MemoryDedupe); inProcess {
		return fmt.Errorf(
			"writer: %d replicas share a stream but deduplicate in process, so a redelivery "+
				"that lands on another replica would be written twice; configure a shared "+
				"deduplication store, or run one replica", replicas)
	}
	return nil
}
