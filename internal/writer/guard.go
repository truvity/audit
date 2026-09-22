package writer

import (
	"fmt"
	"sort"
	"strings"

	"github.com/truvity/audit/preset"
)

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

// GuardKeys refuses a deployment that would write identifiers it never decided
// how to treat.
//
// A profile composed from a framework preset usually asks for external people
// to become pseudonyms. With no key provider there are two honest answers, and
// the deployment has to pick one: configure a provider, or declare that the
// identifiers it receives are already opaque — an identifier an application
// minted, which names nobody without that application's own database.
//
// The third possibility is the one this refuses: starting without keys, never
// saying anything, and writing whatever arrives into an archive nothing can
// edit. See docs/decisions/0013-no-pseudonymisation-keys-by-default.md.
func GuardKeys(profiles map[string]*preset.Profile, hasProvider bool) error {
	if hasProvider {
		return nil
	}
	names := make([]string, 0, len(profiles))
	for name, p := range profiles {
		if p.Identity[preset.External] == preset.Pseudonym {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return fmt.Errorf(
		"writer: profile %s pseudonymises the identifiers of people outside the organisation "+
			"and no key provider is configured. Either configure one, or declare "+
			"`external_identifiers_are_opaque: true` in the deployment document if the "+
			"identifiers reaching this trail are ones an application minted. Starting without "+
			"either would write whatever arrives into an archive nothing can edit",
		strings.Join(names, ", "))
}
