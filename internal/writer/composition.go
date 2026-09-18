package writer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/store"
)

// A profile decides what a copy keeps, how identities in it are treated and how
// long it is kept. Change one and every record written afterwards means
// something different from every record before — and without this, nothing in
// the archive says when that happened. An auditor comparing a record from March
// with one from May would see them differ and blame the emitter.
//
// So the writer keeps each profile's composition in the archive, in full,
// beside the catalogue copies (they describe records too, and are kept as long
// as the longest record), and records the change it is about to start writing
// under. The composition is the answer to "what did this profile keep in
// April"; the event is the answer to "when did that change".

// Composition is one profile's composition as recorded in the archive.
type Composition struct {
	Profile  string `json:"profile"`
	Sequence int    `json:"sequence"`
	// Fingerprint is SHA-256 of the composed profile: every rule it applies.
	Fingerprint string    `json:"fingerprint"`
	ComposedAt  time.Time `json:"composed_at"`
	// Presets are the presets it is composed from, by version. A preset moves
	// when the library is upgraded, which changes what a profile means without
	// anybody editing the profile.
	Presets  map[string]string `json:"presets"`
	Composed *preset.Profile   `json:"composed"`
}

// compositionPrefix is where a profile's compositions live, under the schema
// prefix so a policy that keeps descriptions longer keeps these too.
func compositionPrefix(profile string) string {
	return SchemaPrefix + "/profile/" + profile + "/"
}

// Fingerprint is a composed profile's identity: equal for equal rules, whatever
// the profile is called. The preset versions are not in it — a new preset
// version that composes to the same rules changes no record's meaning, and is
// recorded as a preset change instead.
func Fingerprint(p *preset.Profile) (string, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("writer: fingerprinting profile %s: %w", p.Name, err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// RecordCompositions records each profile's composition, and the change from
// the last one recorded, before the writer takes a record.
//
// It is called once at start-up, outside any write: it records through the
// writer itself, synchronously, and a call from inside a batch would wait on
// its own flush. versions is every preset's version by name.
//
// The first composition a deployment ever records is not a change and emits
// nothing, or the first start of every deployment would claim one. A profile
// whose rules and preset versions are those last recorded writes nothing.
// Otherwise the events are confirmed first and the composition written after:
// a writer that stops between the two records the change again on its next
// start, and a duplicate event is the safer failure than a change the trail
// never mentions — which the other order would risk. A change the trail cannot
// take stops the writer, because these actions are declared block.
func (w *Writer) RecordCompositions(
	ctx context.Context, profiles map[string]*preset.Profile, versions map[string]string,
) error {
	if w.Archive == nil || w.Archive.Store == nil {
		return errors.New("writer: recording compositions needs the schema archive")
	}
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := w.recordComposition(ctx, profiles[name], versions); err != nil {
			return err
		}
	}
	return nil
}

func (w *Writer) recordComposition(ctx context.Context, p *preset.Profile, versions map[string]string) error {
	fingerprint, err := Fingerprint(p)
	if err != nil {
		return err
	}
	used := make(map[string]string, len(p.Presets))
	for _, name := range p.Presets {
		used[name] = versions[name]
	}
	previous, err := w.latestComposition(ctx, p.Name)
	if err != nil {
		return err
	}
	next := Composition{
		Profile: p.Name, Sequence: 1, Fingerprint: fingerprint,
		ComposedAt: w.now(), Presets: used, Composed: p,
	}
	if previous != nil {
		if previous.Fingerprint == fingerprint && sameVersions(previous.Presets, used) {
			return nil
		}
		next.Sequence = previous.Sequence + 1
		if err := w.confirmChanges(ctx, previous, &next); err != nil {
			return err
		}
	}
	body, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	err = w.Archive.Store.Put(ctx, store.Object{
		Key: compositionKey(next), Body: append(body, '\n'),
		RetainUntil: w.Archive.retainUntil(), ContentType: "application/json",
	})
	if errors.Is(err, store.ErrExists) {
		// Another replica recorded this transition first. Its events were
		// recorded too; a second copy of them is the duplicate the ordering
		// above accepts.
		return nil
	}
	if err != nil {
		return fmt.Errorf("writer: recording the composition of %s: %w", p.Name, err)
	}
	return nil
}

// confirmChanges records what differs between two compositions of a profile.
func (w *Writer) confirmChanges(ctx context.Context, previous, next *Composition) error {
	if previous.Fingerprint != next.Fingerprint {
		r := w.meta("audit.profile.changed", auditv1.Operation_OPERATION_MODIFY)
		r.Targets = []*record.Target{{Type: "profile", Id: next.Profile}}
		data, err := structpb.NewStruct(map[string]any{
			"fingerprint":          next.Fingerprint,
			"previous_fingerprint": previous.Fingerprint,
			"sequence":             float64(next.Sequence),
			"composition":          compositionKey(*next),
		})
		if err != nil {
			return err
		}
		r.Data = data
		r.Outcome = &record.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS}
		if err := w.confirm(ctx, r); err != nil {
			return fmt.Errorf("writer: profile %s changed and the trail could not record it: %w", next.Profile, err)
		}
	}
	names := make([]string, 0, len(next.Presets))
	for name := range next.Presets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		was, now := previous.Presets[name], next.Presets[name]
		if was == now {
			continue
		}
		r := w.meta("audit.preset.changed", auditv1.Operation_OPERATION_MODIFY)
		r.Targets = []*record.Target{{Type: "preset", Id: name}}
		data, err := structpb.NewStruct(map[string]any{"version": now, "previous_version": was})
		if err != nil {
			return err
		}
		r.Data = data
		r.Outcome = &record.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS}
		if err := w.confirm(ctx, r); err != nil {
			return fmt.Errorf("writer: preset %s changed and the trail could not record it: %w", name, err)
		}
	}
	return nil
}

// latestComposition reads the most recent composition recorded for a profile,
// or nil when there is none. Keys carry a zero-padded sequence, so the last in
// a listing is the latest.
func (w *Writer) latestComposition(ctx context.Context, profile string) (*Composition, error) {
	entries, err := w.Archive.Store.List(ctx, compositionPrefix(profile), "", 0)
	if err != nil {
		return nil, fmt.Errorf("writer: reading the compositions of %s: %w", profile, err)
	}
	var latest string
	for _, e := range entries {
		if strings.HasSuffix(e.Key, ".json") && e.Key > latest {
			latest = e.Key
		}
	}
	if latest == "" {
		return nil, nil
	}
	body, err := w.Archive.Store.Get(ctx, latest)
	if err != nil {
		return nil, fmt.Errorf("writer: reading %s: %w", latest, err)
	}
	var c Composition
	if err := json.Unmarshal(body, &c); err != nil {
		return nil, fmt.Errorf("writer: reading %s: %w", latest, err)
	}
	return &c, nil
}

func compositionKey(c Composition) string {
	return fmt.Sprintf("%s%08d-%s.json", compositionPrefix(c.Profile), c.Sequence, c.Fingerprint[:16])
}

func sameVersions(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
