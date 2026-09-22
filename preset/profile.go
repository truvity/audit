package preset

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Composition is what a deployment declares: a profile name, the presets it is
// made of, and the prefix its copies land under.
type Composition struct {
	Name    string   `json:"name"`
	Presets []string `json:"presets"`
	Prefix  string   `json:"prefix,omitempty"`
}

// Profile is a composition resolved: what a copy under it carries, how
// identities in it are treated, how long it is kept, and what must be true of
// the store it lands in.
type Profile struct {
	Name    string
	Prefix  string
	Presets []string

	Classes            map[Class]bool
	RequiredFields     []string
	OptionalFields     []string
	ForbiddenFields    []string
	ForbiddenPII       map[string]bool
	RequiredCategories []string

	Identity map[Category]Treatment
	// OpaqueExternal records that the deployment relaxed this profile's
	// external treatment from pseudonym to clear, having declared that the
	// identifiers it receives are already opaque. It is kept so that explaining
	// a profile can say why it keeps what it keeps.
	OpaqueExternal bool
	Retention      Retention
	Integrity      Integrity
	Review         Review
	Pipeline       Pipeline
}

// Compose resolves a composition against the presets available.
//
// Every rule is a union of what the frameworks ask for, resolved towards the
// stricter reading: the strictest identity treatment, the longest retention,
// required over recommended, the most frequent review. A profile is therefore
// never weaker than any framework it claims to satisfy.
func Compose(c Composition, available map[string]*Preset) (*Profile, error) {
	if c.Name == "" {
		return nil, errors.New("profile: name is required")
	}
	if len(c.Presets) == 0 {
		return nil, fmt.Errorf("profile %s: at least one preset is required", c.Name)
	}
	p := &Profile{
		Name:         c.Name,
		Prefix:       c.Prefix,
		Presets:      append([]string(nil), c.Presets...),
		Classes:      map[Class]bool{},
		ForbiddenPII: map[string]bool{},
		Identity:     map[Category]Treatment{},
	}
	if p.Prefix == "" {
		p.Prefix = "profile=" + c.Name
	}
	required, optional, forbidden := map[string]bool{}, map[string]bool{}, map[string]bool{}
	categories := map[string]bool{}

	for _, name := range c.Presets {
		src, ok := available[name]
		if !ok {
			return nil, fmt.Errorf("profile %s: no preset named %q", c.Name, name)
		}
		for _, cl := range src.FieldClasses {
			p.Classes[cl] = true
		}
		for _, f := range src.RequiredFields {
			required[f] = true
		}
		for _, f := range src.OptionalFields {
			optional[f] = true
		}
		for _, f := range src.ForbiddenFields {
			forbidden[f] = true
		}
		for _, pii := range src.ForbiddenPII {
			p.ForbiddenPII[pii] = true
		}
		for _, cat := range src.RequiredCategories {
			categories[cat] = true
		}
		for cat, t := range src.Identity {
			if cur, seen := p.Identity[cat]; !seen || strictness[t] > strictness[cur] {
				p.Identity[cat] = t
			}
		}
		p.Retention = longerRetention(p.Retention, src.Retention)
		p.Integrity = stricterIntegrity(p.Integrity, src.Integrity)
		p.Review = moreFrequentReview(p.Review, src.Review)
		p.Pipeline = widerPipeline(p.Pipeline, src.Pipeline)
	}

	// A field one framework requires and another forbids is a contradiction the
	// deployment has to resolve; guessing which one wins would be the wrong
	// answer either way.
	// Forbidding a field forbids everything beneath it, so /actor and /actor/id
	// are the same conflict. Comparing pointers as plain strings would miss it
	// and quietly produce a copy that one of the frameworks refuses.
	var problems []error
	for f := range forbidden {
		for r := range required {
			if r == f || isUnder(r, f) {
				problems = append(problems, fmt.Errorf(
					"field %s is required by one preset and forbidden by another (as %s)", r, f))
			}
		}
		for o := range optional {
			if o == f || isUnder(o, f) {
				delete(optional, o)
			}
		}
	}
	if err := errors.Join(problems...); err != nil {
		return nil, fmt.Errorf("profile %s: %w", c.Name, err)
	}

	p.RequiredFields = sorted(required)
	p.OptionalFields = sorted(optional)
	p.ForbiddenFields = sorted(forbidden)
	p.RequiredCategories = sorted(categories)
	return p, nil
}

// KeepsField reports whether a core field survives into a copy under this
// profile. A field named by no preset is dropped: a copy carries what its
// purpose justifies, and nothing more.
func KeepsField(p *Profile, pointer string) bool { return p.KeepsField(pointer) }

// KeepsField reports whether a core field, named as a JSON pointer, survives.
func (p *Profile) KeepsField(pointer string) bool {
	for _, f := range p.ForbiddenFields {
		if pointer == f || isUnder(pointer, f) {
			return false
		}
	}
	for _, f := range append(append([]string{}, p.RequiredFields...), p.OptionalFields...) {
		// A listed ancestor keeps its children, and a listed child keeps the
		// ancestor that has to carry it.
		if pointer == f || isUnder(pointer, f) || isUnder(f, pointer) {
			return true
		}
	}
	return false
}

// KeepsProperty reports whether an extension property survives, given the class
// and PII level its schema declares.
func (p *Profile) KeepsProperty(class Class, pii string) bool {
	if pii != "" && p.ForbiddenPII[pii] {
		return false
	}
	if class == "" {
		class = Audit
	}
	return p.Classes[class]
}

// isUnder reports whether child is the same pointer as parent or beneath it,
// comparing whole segments so that /actor does not match /actorial.
func isUnder(child, parent string) bool {
	return strings.HasPrefix(child, parent+"/")
}

// RetainUntil is the date a copy written now may first be deleted. Under an
// after-expiry policy the clock starts at the expiry of whatever the record is
// evidence of; when that is not known at write time the fallback applies and a
// later addendum may extend the lock, which S3 allows and shortening does not.
func (p *Profile) RetainUntil(written time.Time, expiry *time.Time) time.Time {
	r := p.Retention
	if r.Policy == "after_expiry" {
		if expiry != nil {
			return expiry.AddDate(r.YearsAfterExpiry, 0, 0)
		}
		return written.AddDate(0, 0, r.FallbackDays)
	}
	days := r.Days
	if r.MinimumDays > days {
		days = r.MinimumDays
	}
	return written.AddDate(0, 0, days)
}

// Keeps is every core field a copy under this profile carries, in order and
// without repeats: a field may be required by one preset and optional in
// another, and a reader wants the set, not the bookkeeping.
func (p *Profile) Keeps() []string {
	seen := map[string]bool{}
	for _, f := range append(append([]string{}, p.RequiredFields...), p.OptionalFields...) {
		seen[f] = true
	}
	return sorted(seen)
}

// Explain renders the composition as a person reads it, which is what
// `audit profile explain` prints and what a reviewer checks a deployment
// against.
func (p *Profile) Explain() string {
	var b strings.Builder
	fmt.Fprintf(&b, "profile %s\n", p.Name)
	fmt.Fprintf(&b, "  presets   %s\n", strings.Join(p.Presets, ", "))
	fmt.Fprintf(&b, "  prefix    %s\n", p.Prefix)
	classes := make([]string, 0, len(p.Classes))
	for c := range p.Classes {
		classes = append(classes, string(c))
	}
	sort.Strings(classes)
	fmt.Fprintf(&b, "  classes   %s\n", strings.Join(classes, ", "))
	fmt.Fprintf(&b, "  keeps     %s\n", strings.Join(p.Keeps(), " "))
	if len(p.ForbiddenFields) > 0 {
		fmt.Fprintf(&b, "  never     %s\n", strings.Join(p.ForbiddenFields, " "))
	}
	for _, c := range []Category{Internal, External, Machine} {
		note := ""
		if c == External && p.OpaqueExternal {
			note = "  (relaxed from pseudonym: the deployment declares external identifiers opaque)"
		}
		fmt.Fprintf(&b, "  identity  %-8s %s%s\n", c, p.Identity[c], note)
	}
	switch p.Retention.Policy {
	case "after_expiry":
		fmt.Fprintf(&b, "  retention %d years after expiry (fallback %d days), %d days hot\n",
			p.Retention.YearsAfterExpiry, p.Retention.FallbackDays, p.Retention.HotDays)
	default:
		fmt.Fprintf(&b, "  retention %d days (never below %d), %d days hot\n",
			p.Retention.Days, p.Retention.MinimumDays, p.Retention.HotDays)
	}
	fmt.Fprintf(&b, "  integrity digest %s, object lock %s, clock %s\n",
		p.Integrity.Digest, p.Integrity.ObjectLockMode, p.Integrity.ClockSyncEvent)
	if p.Review.Cadence != "" {
		fmt.Fprintf(&b, "  review    %s\n", p.Review.Cadence)
	}
	if len(p.RequiredCategories) > 0 {
		fmt.Fprintf(&b, "  requires  %s\n", strings.Join(p.RequiredCategories, ", "))
	}
	return b.String()
}

func longerRetention(a, b Retention) Retention {
	if a.Policy == "" {
		return b
	}
	out := a
	// An after-expiry policy outlasts any fixed number of days, because what it
	// waits for has not happened yet.
	if b.Policy == "after_expiry" && a.Policy != "after_expiry" {
		out.Policy = b.Policy
		out.YearsAfterExpiry = b.YearsAfterExpiry
		out.FallbackDays = max(b.FallbackDays, a.Days)
	} else if b.Policy == "after_expiry" {
		out.YearsAfterExpiry = max(a.YearsAfterExpiry, b.YearsAfterExpiry)
		out.FallbackDays = max(a.FallbackDays, b.FallbackDays)
	}
	out.Days = max(a.Days, b.Days)
	out.HotDays = max(a.HotDays, b.HotDays)
	out.MinimumDays = max(a.MinimumDays, b.MinimumDays)
	out.PublishedInTerms = a.PublishedInTerms || b.PublishedInTerms
	// If any framework says the copy must go at the end, it goes.
	out.DeleteAtEnd = a.DeleteAtEnd || b.DeleteAtEnd
	out.Configurable = a.Configurable && b.Configurable
	if out.Note == "" {
		out.Note = b.Note
	}
	return out
}

func stricterIntegrity(a, b Integrity) Integrity {
	return Integrity{
		Digest:          stricter(a.Digest, b.Digest, "recommended", "required"),
		ObjectLockMode:  stricter(a.ObjectLockMode, b.ObjectLockMode, lockOrder...),
		TimestampAnchor: stricter(a.TimestampAnchor, b.TimestampAnchor, "none", "recommended", "required"),
		LegalHold:       stricter(a.LegalHold, b.LegalHold, "available", "recommended"),
		ClockSyncEvent:  stricter(a.ClockSyncEvent, b.ClockSyncEvent, "none", "daily"),
		LogAccessLogged: a.LogAccessLogged || b.LogAccessLogged,
		Note:            lockNote(a, b),
	}
}

// lockNote keeps the note of whichever preset's lock reading won, so that the
// composed profile explains the mode it ended up with and not the one it
// discarded. Equal readings keep the first, as the retention note does.
func lockNote(a, b Integrity) string {
	if LockRank(b.ObjectLockMode) > LockRank(a.ObjectLockMode) {
		return b.Note
	}
	return firstNonEmpty(a.Note, b.Note)
}

func moreFrequentReview(a, b Review) Review {
	return Review{
		AutomatedAlerting: a.AutomatedAlerting || b.AutomatedAlerting,
		Cadence:           stricter(a.Cadence, b.Cadence, "quarterly", "monthly", "weekly", "daily"),
		Evidence:          firstNonEmpty(a.Evidence, b.Evidence),
	}
}

func widerPipeline(a, b Pipeline) Pipeline {
	out := Pipeline{
		DedupeWindowDays:  max(a.DedupeWindowDays, b.DedupeWindowDays),
		StreamHorizonDays: max(a.StreamHorizonDays, b.StreamHorizonDays),
	}
	// The earliest close wins: a period that one framework considers shut must
	// not still be open under another.
	switch {
	case a.CloseAfterHours == 0:
		out.CloseAfterHours = b.CloseAfterHours
	case b.CloseAfterHours == 0:
		out.CloseAfterHours = a.CloseAfterHours
	default:
		out.CloseAfterHours = min(a.CloseAfterHours, b.CloseAfterHours)
	}
	return out
}

// stricter returns whichever of a and b comes later in order, where order runs
// from the weakest reading to the strictest.
func stricter(a, b string, order ...string) string {
	rank := func(v string) int {
		for i, o := range order {
			if v == o {
				return i
			}
		}
		return -1
	}
	if rank(b) > rank(a) {
		return b
	}
	if a == "" {
		return b
	}
	return a
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
