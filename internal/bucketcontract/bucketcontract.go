// Package bucketcontract checks a bucket against the v1 bucket contract,
// docs/reference/bucket-contract.md: the key grammar, the envelope of every
// line, the metadata of every object, its sha256, the hash of every record, and
// the order keys sort in.
//
// It reads and never writes, takes any store.Store, and so runs against the
// in-memory store in a unit test, against a LocalStack bucket in CI, and
// against a real bucket from a person's terminal. It is the checker the
// conformance suite in this package's tests applies to what the writer writes,
// and the part of `audit verify` that is about the contract and not about the
// deployment: a second implementation of ingest, or of a reader, is held to
// the same rules by the same code.
//
// Every rule has a name, and a finding carries it, so that a failure says which
// sentence of the contract was broken.
package bucketcontract

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/truvity/audit/internal/recobj"
	"github.com/truvity/audit/internal/ulid"
	"github.com/truvity/audit/store"
)

// The rules, as findings name them.
const (
	// RuleKey: a key under records/ is records/<profile>/<tenant>/<yyyy>/<mm>/
	// <dd>/<hh>/<ULID>, and the ULID was made in the hour the key names.
	RuleKey = "key.grammar"
	// RuleCatalogueKey: a key under catalogue/ is catalogue/<app>/<version>.
	RuleCatalogueKey = "key.catalogue"
	// RuleUnique: two batches never share a ULID.
	RuleUnique = "key.ulid-unique"
	// RuleOrder: keys sort by ingest time, and a listing returns them so.
	RuleOrder = "key.order"
	// RuleRead: an object can be read, whole.
	RuleRead = "object.read"
	// RuleMetadata: format, sha256 and count are present and say what the
	// object is.
	RuleMetadata = "object.metadata"
	// RuleSHA256: the sha256 metadata is the SHA-256 of the stored bytes.
	RuleSHA256 = "object.sha256"
	// RuleBody: the body is zstd-compressed newline-delimited JSON, one
	// {"hash", "record"} envelope a line.
	RuleBody = "object.body"
	// RuleCount: the count metadata is the number of lines.
	RuleCount = "object.count"
	// RuleHash: each line's hash is the SHA-256 of its record's canonical form.
	RuleHash = "record.hash"
	// RulePlacement: a record is under its own profile and tenant.
	RulePlacement = "record.placement"
	// RuleCatalogue: the catalogue a record names is in the bucket.
	RuleCatalogue = "catalogue.present"
	// RuleCatalogueBody: a catalogue object is not empty.
	RuleCatalogueBody = "catalogue.body"
)

// Finding is one rule broken, by one key.
type Finding struct {
	Rule   string `json:"rule"`
	Key    string `json:"key"`
	Detail string `json:"detail"`
	// Hour is the hour of ingest time the finding is about, when it is about
	// one: the findings of the seals are.
	Hour time.Time `json:"hour,omitzero"`
}

func (f Finding) String() string { return f.Rule + ": " + f.Key + ": " + f.Detail }

// Result is what one object was found to hold, and what is wrong with it.
type Result struct {
	// Entry is the object as a HEAD sees it: its metadata, retention and hold.
	Entry    store.Entry
	Records  int
	Findings []Finding
	// Sources are the catalogues the records name, as source@version.
	Catalogues map[string]bool
}

// CheckObject checks one record object against the contract: its key, its
// metadata, its bytes and every record in it. A store failure is a finding of
// the object.read rule and not an error: an object that cannot be read is
// something the contract says is wrong with the bucket.
//
// profile and tenant, when not empty, are what the object is expected to be
// under, as a walk of a prefix knows; a key under another is a finding.
func CheckObject(ctx context.Context, s store.Store, key, profile, tenant string) Result {
	res := Result{Catalogues: map[string]bool{}}
	bad := func(rule, format string, args ...any) {
		res.Findings = append(res.Findings, Finding{Rule: rule, Key: key, Detail: fmt.Sprintf(format, args...)})
	}

	parsed, ok := store.ParseRecordKey(key)
	if !ok {
		bad(RuleKey, "the key is not records/<profile>/<tenant>/<yyyy>/<mm>/<dd>/<hh>/<ULID>, "+
			"or its ULID is not from the hour it names")
		return res
	}
	if (profile != "" && parsed.Profile != profile) || (tenant != "" && parsed.Tenant != tenant) {
		bad(RuleKey, "the key names profile %s and tenant %s", parsed.Profile, parsed.Tenant)
		return res
	}

	head, err := s.Head(ctx, key)
	if err != nil {
		bad(RuleRead, "could not be read: %v", err)
		return res
	}
	res.Entry = head
	body, err := s.Get(ctx, key)
	if err != nil {
		bad(RuleRead, "could not be read: %v", err)
		return res
	}

	lines, err := recobj.Decode(body)
	if err != nil {
		bad(RuleBody, "%v", err)
		// The sha256 is still worth saying: it tells a reader whether the
		// bytes are what was written or something else that does not decode.
		if got := head.Metadata[store.MetaSHA256]; got != recobj.SHA256(body) {
			bad(RuleSHA256, "sha256 is %q, the object hashes to %s", got, recobj.SHA256(body))
		}
		return res
	}
	res.Records = len(lines)

	meta := head.Metadata
	if got := meta[store.MetaFormat]; got != store.Format {
		bad(RuleMetadata, "format is %q, not %q", got, store.Format)
	}
	if got := meta[store.MetaSHA256]; got == "" {
		bad(RuleMetadata, "there is no sha256")
	} else if want := recobj.SHA256(body); got != want {
		bad(RuleSHA256, "sha256 is %q, the object hashes to %s", got, want)
	}
	if got, want := meta[store.MetaCount], fmt.Sprint(len(lines)); got == "" {
		bad(RuleMetadata, "there is no count")
	} else if got != want {
		bad(RuleCount, "count is %q, the object has %d records", got, len(lines))
	}

	for n, line := range lines {
		if err := line.Verify(); err != nil {
			bad(RuleHash, "line %d: %v", n+1, err)
			continue
		}
		copied, err := line.Decoded()
		if err != nil {
			bad(RuleBody, "line %d: not a record: %v", n+1, err)
			continue
		}
		if copied.GetProfile() != parsed.Profile {
			bad(RulePlacement, "line %d: a copy under profile %q is in profile %s's prefix",
				n+1, copied.GetProfile(), parsed.Profile)
		}
		if t := copied.GetTenantId(); t != "" && t != parsed.Tenant {
			bad(RulePlacement, "line %d: a record of tenant %q is under tenant %s", n+1, t, parsed.Tenant)
		}
		res.Catalogues[copied.GetSource()+"@"+copied.GetCatalogueVersion()] = true
	}
	return res
}

// Options narrow a Check.
type Options struct {
	// Profiles to check; empty is every profile the bucket has records for.
	Profiles []string
	// From and To bound the ingest hours checked, both inclusive; zero is
	// unbounded.
	From, To time.Time
	// Skew, when set, also requires an object's ULID to be within this long of
	// when the bucket says it was written. It is off by default: a writer's
	// clock and a bucket's are two clocks, and a test that fixes the writer's
	// has no use for it.
	Skew time.Duration
}

// Report is what a Check found.
type Report struct {
	Objects, Records, Catalogues int
	Findings                     []Finding
}

// OK reports whether the bucket conforms.
func (r *Report) OK() bool { return len(r.Findings) == 0 }

// String lists the findings, or says the bucket conforms.
func (r *Report) String() string {
	var b strings.Builder
	for _, f := range r.Findings {
		b.WriteString(f.String() + "\n")
	}
	fmt.Fprintf(&b, "%d objects, %d records, %d catalogues, %d findings", r.Objects, r.Records, r.Catalogues, len(r.Findings))
	return b.String()
}

// Has reports whether any finding is of the rule.
func (r *Report) Has(rule string) bool {
	for _, f := range r.Findings {
		if f.Rule == rule {
			return true
		}
	}
	return false
}

// Check reads a bucket and holds it to the contract.
func Check(ctx context.Context, s store.Store, o Options) (*Report, error) {
	report := &Report{}
	from, to := o.From, o.To
	if from.IsZero() {
		from = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if to.IsZero() {
		to = time.Date(9000, 1, 1, 0, 0, 0, 0, time.UTC)
	}

	profiles := o.Profiles
	if len(profiles) == 0 {
		var err error
		if profiles, err = store.Profiles(ctx, s); err != nil {
			return nil, err
		}
	}

	seen := map[string]string{} // ULID -> the first key that had it
	wanted := map[string]string{}
	for _, profile := range profiles {
		tenants, err := store.Tenants(ctx, s, profile)
		if err != nil {
			return nil, err
		}
		for _, tenant := range tenants {
			previous := ""
			err := store.WalkHours(ctx, s, profile, tenant, from, to, "", func(e store.Entry) error {
				report.Objects++
				if previous != "" && e.Key <= previous {
					report.Findings = append(report.Findings, Finding{
						Rule: RuleOrder, Key: e.Key, Detail: "a listing returned this key after " + previous,
					})
				}
				previous = e.Key

				res := CheckObject(ctx, s, e.Key, profile, tenant)
				report.Records += res.Records
				report.Findings = append(report.Findings, res.Findings...)
				for c := range res.Catalogues {
					wanted[c] = e.Key
				}

				if parsed, ok := store.ParseRecordKey(e.Key); ok {
					if first, dup := seen[parsed.ULID]; dup {
						report.Findings = append(report.Findings, Finding{
							Rule: RuleUnique, Key: e.Key, Detail: "the ULID is also the key of " + first,
						})
					}
					seen[parsed.ULID] = e.Key
					if o.Skew > 0 && !res.Entry.Modified.IsZero() {
						at, _ := ulid.Time(parsed.ULID)
						if d := res.Entry.Modified.Sub(at); d > o.Skew || d < -o.Skew {
							report.Findings = append(report.Findings, Finding{
								Rule: RuleOrder, Key: e.Key,
								Detail: fmt.Sprintf("the ULID is from %s and the bucket wrote the object at %s",
									at.Format(time.RFC3339), res.Entry.Modified.Format(time.RFC3339)),
							})
						}
					}
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
		}
	}

	held, err := checkCatalogues(ctx, s, report)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(wanted))
	for c := range wanted {
		keys = append(keys, c)
	}
	sort.Strings(keys)
	for _, c := range keys {
		source, version, _ := strings.Cut(c, "@")
		if !held[store.CatalogueKey(source, version)] {
			report.Findings = append(report.Findings, Finding{
				Rule: RuleCatalogue, Key: wanted[c],
				Detail: fmt.Sprintf("it names catalogue %s version %s, and %s is not in the bucket",
					source, version, store.CatalogueKey(source, version)),
			})
		}
	}
	return report, nil
}

// checkCatalogues checks the keys and bodies under catalogue/ and returns the
// keys that are well-formed.
func checkCatalogues(ctx context.Context, s store.Store, report *Report) (map[string]bool, error) {
	entries, err := s.List(ctx, store.CataloguePrefix, "", 0)
	if err != nil {
		return nil, err
	}
	held := map[string]bool{}
	for _, e := range entries {
		report.Catalogues++
		parts := strings.Split(strings.TrimPrefix(e.Key, store.CataloguePrefix), "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			report.Findings = append(report.Findings, Finding{
				Rule: RuleCatalogueKey, Key: e.Key, Detail: "the key is not catalogue/<app>/<version>",
			})
			continue
		}
		body, err := s.Get(ctx, e.Key)
		if err != nil {
			report.Findings = append(report.Findings, Finding{
				Rule: RuleRead, Key: e.Key, Detail: fmt.Sprintf("could not be read: %v", err),
			})
			continue
		}
		if len(body) == 0 {
			report.Findings = append(report.Findings, Finding{
				Rule: RuleCatalogueBody, Key: e.Key, Detail: "the catalogue is empty",
			})
			continue
		}
		held[e.Key] = true
	}
	return held, nil
}
