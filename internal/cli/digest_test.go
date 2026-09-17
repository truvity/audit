package cli_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/internal/digest"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/store"
	"github.com/truvity/audit/store/storetest"
)

func sealer(t *testing.T, s *storetest.Memory, now time.Time) cli.Digest {
	t.Helper()
	signer, err := keys.NewLocalSigner("test")
	if err != nil {
		t.Fatal(err)
	}
	presets, err := preset.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := (&cli.Deployment{
		Profiles: map[string]cli.ProfileConfig{"security": {Presets: []string{"security"}}},
	}).Compose(presets)
	if err != nil {
		t.Fatal(err)
	}
	return cli.Digest{
		Store: s, Signer: signer, Profiles: profiles,
		Now: func() time.Time { return now }, Out: &strings.Builder{},
	}
}

// object writes an archive object as the writer would, appearing at a moment,
// so that a window can be about write time.
func archived(t *testing.T, s *storetest.Memory, tenant string, written time.Time, name string) string {
	t.Helper()
	key := "profile=security/tenant=" + tenant + "/year=" + written.Format("2006") +
		"/month=" + written.Format("01") + "/day=" + written.Format("02") + "/" + name
	s.Now = func() time.Time { return written }
	if err := s.Put(context.Background(), store.Object{
		Key: key, Body: []byte("{}"), RetainUntil: written.AddDate(1, 0, 0),
	}); err != nil {
		t.Fatal(err)
	}
	return key
}

// A job that missed its runs must seal the windows it missed. A gap in the
// chain cannot be told from a digest somebody removed, so leaving one is the
// same as leaving evidence of tampering nobody can resolve.
func TestDigestCatchesUpOnWindowsAMissedRunLeft(t *testing.T) {
	s := storetest.NewMemory()
	start := at(t, "2026-09-17T10:00:00Z")

	// One window is sealed, then three hours pass with objects written in them.
	run := sealer(t, s, start.Add(time.Hour))
	archived(t, s, "acme", start.Add(30*time.Minute), "a.ndjson.zst")
	if _, err := run.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	for hour := 1; hour <= 3; hour++ {
		archived(t, s, "acme", start.Add(time.Duration(hour)*time.Hour+30*time.Minute),
			"h"+string(rune('0'+hour))+".ndjson.zst")
	}
	run = sealer(t, s, start.Add(4*time.Hour))
	report, err := run.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Profiles[0].Sealed; got != 3 {
		t.Fatalf("sealed %d windows, want the 3 the missed runs left", got)
	}
	for hour := 1; hour <= 3; hour++ {
		key := digest.Key("security", start.Add(time.Duration(hour)*time.Hour))
		if _, err := s.Head(context.Background(), key); err != nil {
			t.Fatalf("window %d was not sealed: %v", hour, err)
		}
	}
}

// The hour the job wakes in is still being written into. Sealing it would
// produce a digest that omits the objects still to come, and the verifier would
// rightly report them as unaccounted for.
func TestDigestLeavesTheOpenHourAlone(t *testing.T) {
	s := storetest.NewMemory()
	start := at(t, "2026-09-17T10:00:00Z")
	archived(t, s, "acme", start.Add(30*time.Minute), "a.ndjson.zst")

	run := sealer(t, s, start.Add(90*time.Minute))
	if _, err := run.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Head(context.Background(), digest.Key("security", start.Add(time.Hour))); err == nil {
		t.Fatal("the hour still open was sealed")
	}
	if _, err := s.Head(context.Background(), digest.Key("security", start)); err != nil {
		t.Fatalf("the hour that closed was not sealed: %v", err)
	}
}

// Re-running a range that is already sealed is the ordinary case: an operator
// backfilling does not know exactly where the gap starts. Resuming, the second
// run has nothing to look at; asked for the range explicitly, it finds the
// window sealed and leaves it, rather than putting a second digest at a key the
// archive would refuse anyway.
func TestDigestRunningTwiceSealsOnce(t *testing.T) {
	s := storetest.NewMemory()
	start := at(t, "2026-09-17T10:00:00Z")
	archived(t, s, "acme", start.Add(30*time.Minute), "a.ndjson.zst")

	run := sealer(t, s, start.Add(time.Hour))
	first, err := run.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Profiles[0].Sealed != 1 {
		t.Fatalf("the first run sealed %d", first.Profiles[0].Sealed)
	}

	// Resuming: the chain is up to date, so there is nothing to seal.
	resumed, err := run.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Profiles[0].Sealed != 0 {
		t.Fatalf("resuming sealed %d windows again", resumed.Profiles[0].Sealed)
	}

	// Asked for the range: the window is there and is left alone.
	run.From = start
	again, err := run.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if again.Profiles[0].Sealed != 0 || again.Profiles[0].Existing != 1 {
		t.Fatalf("re-running the range sealed %d and found %d already sealed",
			again.Profiles[0].Sealed, again.Profiles[0].Existing)
	}
}

// A run that has a month to catch up on says how much is left rather than
// running until somebody kills it.
func TestDigestBoundsOneRunAndSaysWhatIsLeft(t *testing.T) {
	s := storetest.NewMemory()
	start := at(t, "2026-09-17T10:00:00Z")
	archived(t, s, "acme", start.Add(30*time.Minute), "a.ndjson.zst")

	run := sealer(t, s, start.Add(10*time.Hour))
	run.From = start
	run.MaxWindows = 4
	report, err := run.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Profiles[0].Sealed != 4 {
		t.Fatalf("sealed %d, want the bound of 4", report.Profiles[0].Sealed)
	}
	if report.Profiles[0].Remaining != 6 {
		t.Fatalf("says %d remaining, want 6", report.Profiles[0].Remaining)
	}
}

// The chain it writes has to be the chain the verifier accepts, across tenants.
func TestDigestProducesAChainThatVerifies(t *testing.T) {
	s := storetest.NewMemory()
	start := at(t, "2026-09-17T10:00:00Z")
	archived(t, s, "acme", start.Add(10*time.Minute), "a.ndjson.zst")
	archived(t, s, "globex", start.Add(20*time.Minute), "b.ndjson.zst")
	archived(t, s, "acme", start.Add(70*time.Minute), "c.ndjson.zst")

	// A first run seals only the hour that has just closed, because sealing
	// every hour since the archive began is not what a job waking up should do.
	// An operator backfilling names the range.
	run := sealer(t, s, start.Add(2*time.Hour))
	run.From = start
	if _, err := run.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	signer, ok := run.Signer.(*keys.LocalSigner)
	if !ok {
		t.Fatalf("%T", run.Signer)
	}
	pub, err := signer.PublicKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	report, err := (&digest.Verifier{Store: s, PublicKeyPEM: pub}).
		Verify(context.Background(), "security", start, start.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if problems := report.Problems(); len(problems) != 0 {
		t.Fatalf("the chain this command wrote does not verify: %v", problems)
	}
	if report.Objects != 3 {
		t.Fatalf("the chain accounts for %d objects, want 3 across both tenants", report.Objects)
	}
}

// An unsigned chain proves nothing, so the command refuses rather than writing
// one that looks like evidence.
func TestDigestRefusesWithoutASigner(t *testing.T) {
	run := sealer(t, storetest.NewMemory(), at(t, "2026-09-17T10:00:00Z"))
	run.Signer = nil
	if _, err := run.Run(context.Background()); err == nil {
		t.Fatal("a digest run without a signer must be refused")
	}
}

// A job waking for the first time seals the hour that just closed, not every
// hour since the archive began. The rest is a backfill an operator asks for.
func TestDigestFirstRunSealsOnlyTheHourThatClosed(t *testing.T) {
	s := storetest.NewMemory()
	start := at(t, "2026-09-17T10:00:00Z")
	for hour := 0; hour < 5; hour++ {
		archived(t, s, "acme", start.Add(time.Duration(hour)*time.Hour+30*time.Minute),
			"h"+string(rune('0'+hour))+".ndjson.zst")
	}

	run := sealer(t, s, start.Add(5*time.Hour))
	report, err := run.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Profiles[0].Sealed != 1 {
		t.Fatalf("a first run sealed %d windows, want only the hour that closed", report.Profiles[0].Sealed)
	}
	if _, err := s.Head(context.Background(), digest.Key("security", start.Add(4*time.Hour))); err != nil {
		t.Fatalf("the hour that closed was not the one sealed: %v", err)
	}
}
