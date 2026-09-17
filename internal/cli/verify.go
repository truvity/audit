package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/truvity/audit/internal/digest"
	"github.com/truvity/audit/store"
)

// Verify walks a profile's digest chain and reports what it found.
//
// It needs the archive and a public key and nothing else. An auditor runs it
// with read-only credentials and their own copy of this command, which is what
// makes the answer worth having: nothing in the result depends on trusting the
// operator of the archive.
type Verify struct {
	Store        store.Store
	PublicKeyPEM []byte
	Profile      string
	From, To     time.Time
	// MinimumRetention lets the check also say whether an object's lock is
	// shorter than its profile requires.
	MinimumRetention map[string]time.Duration
	JSON             bool
	Out              io.Writer
}

// Run reports the number of problems found.
func (v Verify) Run(ctx context.Context) (int, error) {
	out := v.Out
	if out == nil {
		out = os.Stdout
	}
	verifier := &digest.Verifier{
		Store:            v.Store,
		PublicKeyPEM:     v.PublicKeyPEM,
		MinimumRetention: v.MinimumRetention,
	}
	report, err := verifier.Verify(ctx, v.Profile, v.From, v.To)
	if err != nil {
		return 0, err
	}
	if v.JSON {
		body, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return 0, err
		}
		printf(out, "%s\n", body)
	} else {
		printf(out, "%s", report.String())
	}
	return len(report.Problems()), nil
}

// ParseDay reads a date or a timestamp, so that an auditor may write either.
func ParseDay(v string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15", "2006-01-02"} {
		if at, err := time.Parse(layout, v); err == nil {
			return at.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a date or a timestamp", v)
}
