// Package suite is the ONE end-to-end suite for this repository's kind
// tier: it proves the chart works against real servers, through Service
// endpoints, the way truvity/policy's own example suite does — see that
// repository's docs/guides/testing.md for the pattern this one borrows.
//
// It is inert unless E2E_NAMESPACE is set, so `go test ./...` (and
// therefore `just check`) never touches a network or a cluster. Names come
// from e2e/fixture's own resolver, never repeated here by hand: a values
// file that renames a Secret or a role changes what this suite asks for
// the next time it runs, with nothing here to edit.
package suite

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// shared is resolved once, in TestMain, and read by every test — never
// mutated after that point, so tests may run in any order.
var shared env

func TestMain(m *testing.M) {
	namespace, run := namespaceFromEnv()
	if !run {
		fmt.Fprintf(os.Stderr, "suite: %s is not set — skipping the cluster suite\n", envNamespace)
		os.Exit(0)
	}

	resolved, err := resolveEnvWithTimeout(namespace, 30*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "suite: %v\n", err)
		os.Exit(1)
	}
	shared = resolved

	code := m.Run()

	shared.cluster.CloseForwards()

	os.Exit(code)
}

// eventually polls check, bounded by patience, and fails the test with the
// last error once patience runs out. Every asynchronous assertion in this
// suite shares this shape: a write is accepted synchronously, but what it
// causes — the index row, the stream hop — is proved by waiting, not by
// asserting once.
func eventually(t *testing.T, patience time.Duration, check func() error) {
	t.Helper()

	deadline := time.Now().Add(patience)
	var last error
	for {
		err := check()
		if err == nil {
			return
		}
		last = err
		if time.Now().After(deadline) {
			t.Fatalf("did not become true within %s: %v", patience, last)
		}
		time.Sleep(time.Second)
	}
}
