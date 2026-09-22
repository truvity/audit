package main

import (
	"strings"
	"testing"
)

// The chart gives every scheduled job the archive in the environment and
// nothing on the command line. A subcommand that reads only its flag then
// fails with `name the archive's bucket with --bucket`, which reads as a
// deployment that forgot an argument rather than a binary that ignored one --
// and it fails on a schedule, hours after anybody was looking.
//
// Each case below gives the arguments the subcommand checks BEFORE the bucket,
// so that reaching any other error at all is the proof: the bucket was found.
func TestEverySubcommandTakesTheArchiveFromTheEnvironment(t *testing.T) {
	const missing = "name the archive's bucket"

	for name, c := range map[string]struct {
		run  func([]string) error
		args []string
	}{
		"verify":  {verify, []string{"--profile=security", "--public-key=/dev/null"}},
		"replay":  {replay, []string{"--dlq"}},
		"reindex": {reindex, []string{"--profile=security", "--database=postgres://x/y"}},
		"digest":  {digestCmd, []string{"--deployment=/dev/null"}},
		"hold":    {holdCmd, []string{"list"}},
		"key":     {keyCmd, []string{"destroy"}},
	} {
		t.Run(name, func(t *testing.T) {
			// Set for this test only; the runtime restores it afterwards.
			t.Setenv("AUDIT_BUCKET", "an-archive")

			err := c.run(c.args)
			if err != nil && strings.Contains(err.Error(), missing) {
				t.Errorf("AUDIT_BUCKET is set and %s still says %q", name, err)
			}
		})
	}
}

// Without it, the refusal is still the one that says which argument is missing.
func TestTheRefusalStillNamesTheBucket(t *testing.T) {
	t.Setenv("AUDIT_BUCKET", "")

	err := verify([]string{"--profile=security", "--public-key=/dev/null"})
	if err == nil || !strings.Contains(err.Error(), "name the archive's bucket") {
		t.Errorf("got %v, want a refusal naming the bucket", err)
	}
}
