package cli

import (
	"context"
	"time"

	"github.com/truvity/audit/internal/bucketcontract"
	"github.com/truvity/audit/store"
)

func (v Verify) now() time.Time {
	if v.Now != nil {
		return v.Now().UTC()
	}
	return time.Now().UTC()
}

// checkSeals holds the seals of the range to the contract, with the roots the
// verifier was given and nothing else trusted, and adds what it finds to the
// report. What a seal is held to is internal/bucketcontract's: the same rules
// the conformance suite applies to a notary, so that a second implementation of
// the notary is checked by the same code that checks this one.
func (v Verify) checkSeals(ctx context.Context, report *VerifyReport, windows map[string]bool) error {
	res, err := bucketcontract.CheckSeals(ctx, v.Store, bucketcontract.SealOptions{
		Profile: v.Profile, Roots: v.Seals.Roots,
		From: v.From.UTC(), To: v.To.UTC().Add(-time.Nanosecond),
		Now: v.now(), Settle: v.Seals.Settle, Grace: v.Seals.Grace,
		Lock: v.RequiredLock[v.Profile],
	})
	if err != nil {
		return err
	}
	report.Seals = res.Seals
	window := func(hour time.Time) string {
		if hour.IsZero() {
			return store.KeysPrefix
		}
		w := store.ProfilePrefix(v.Profile) + hour.Format("2006/01/02/15")
		if !windows[w] {
			windows[w] = true
			report.Windows = append(report.Windows, w)
		}
		return w
	}
	// An hour with a seal and no objects is a window checked too.
	for hour := range res.Hours {
		window(hour)
	}
	for _, f := range res.Findings {
		report.Findings = append(report.Findings, VerifyFinding{
			Window: window(f.Hour), Object: f.Key, Reason: f.Rule + ": " + f.Detail,
		})
	}
	for _, ok := range res.Clean {
		report.Findings = append(report.Findings, VerifyFinding{Window: window(ok.Hour), Object: ok.Key, OK: true})
	}
	return nil
}
