package cli

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/truvity/audit/internal/seal"
	"github.com/truvity/audit/internal/telemetry"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/sdk/catalogue"
	auditv1 "github.com/truvity/audit/sdk/gen/audit/v1"
	"github.com/truvity/audit/sdk/record"
	"github.com/truvity/audit/sdk/sink"
	"github.com/truvity/audit/store"
)

// rootsRetention is the retention keys/roots.jwks is put with when the notary
// bootstraps it. The contract does not lock the file (the operator owns it, and
// a bucket versions it), but a store opened with an Object Lock mode refuses a
// put with none; a day is as short as is useful.
const rootsRetention = 24 * time.Hour

// Notary seals the archive: for every profile, every tenant and every closed
// hour since the last seal, one signed statement of what the hour holds, chained
// to the one before (docs/reference/bucket-contract.md, docs/decisions/0019).
//
// It is the one part of the system that signs, and it holds nothing the writer
// does: the key is a managed one the writer's role cannot use, and the bucket
// rights are to read records and put seals. Whoever writes the archive and can
// also sign for it can choose what to sign, which is the whole of why this is
// a separate job under a separate identity.
//
// A run is idempotent. An hour already sealed is not sealed again, and a run
// over a bucket with nothing new does nothing but read.
type Notary struct {
	Store  store.Store
	Signer keys.Signer
	// Profiles to seal; empty is every profile the archive has records for.
	Profiles []string
	// Settle is how long after an hour has ended it is sealed, so that a batch
	// put late in the hour it is keyed by is in the seal.
	Settle time.Duration
	// Now is the clock; nil is the wall clock.
	Now func() time.Time
	// Metrics, when given, receives the age of the newest seal per profile.
	Metrics *telemetry.Notary
	// Sink and Catalogue, when both are given, are where each seal is recorded.
	Sink      sink.Sink
	Catalogue *catalogue.Catalogue
	Version   string
	Instance  string
	JSON      bool
	Out       io.Writer
}

// SealedHour is one seal this run wrote.
type SealedHour struct {
	Key     string    `json:"key"`
	Profile string    `json:"profile"`
	Tenant  string    `json:"tenant"`
	Hour    time.Time `json:"hour"`
	Objects int       `json:"objects"`
	Records int64     `json:"records"`
	Root    string    `json:"root"`
}

// NotaryFailure is a tenant the run could not seal further, and why. The chain
// stops there: a seal over a later hour would be chained to a seal that does
// not exist.
type NotaryFailure struct {
	Profile string `json:"profile"`
	Tenant  string `json:"tenant"`
	Reason  string `json:"reason"`
}

// NotaryReport is what a run did.
type NotaryReport struct {
	// Key is the thumbprint of the key the seals were signed with.
	Key string `json:"key"`
	// Sealed are the seals written; Present counts the hours found sealed
	// already by another run, which is what makes a rerun a no-op.
	Sealed   []SealedHour    `json:"sealed"`
	Present  int             `json:"present"`
	Failures []NotaryFailure `json:"failures,omitempty"`
	// Ages is, per profile, the seconds since the end of the newest sealed hour
	// of the tenant furthest behind.
	Ages map[string]float64 `json:"ages,omitempty"`
}

func (n Notary) now() time.Time {
	if n.Now != nil {
		return n.Now().UTC()
	}
	return time.Now().UTC()
}

// Run seals what can be sealed and reports it. A tenant that cannot be sealed
// is a failure in the report and an error from Run, and the other tenants are
// sealed regardless.
func (n Notary) Run(ctx context.Context) (*NotaryReport, error) {
	out := n.Out
	if out == nil {
		out = os.Stdout
	}
	report, err := n.run(ctx)
	if report != nil {
		if n.JSON {
			body, merr := json.MarshalIndent(report, "", "  ")
			if merr != nil {
				return report, merr
			}
			printf(out, "%s\n", body)
		} else {
			printf(out, "%s", report)
		}
	}
	return report, err
}

func (r *NotaryReport) String() string {
	var s string
	for _, h := range r.Sealed {
		s += fmt.Sprintf("sealed   %s: %d objects, %d records, root %s\n", h.Key, h.Objects, h.Records, h.Root)
	}
	for _, f := range r.Failures {
		s += fmt.Sprintf("FAILED   %s/%s: %s\n", f.Profile, f.Tenant, f.Reason)
	}
	return s + fmt.Sprintf("%d sealed, %d already sealed, %d tenants failed; key %s\n",
		len(r.Sealed), r.Present, len(r.Failures), r.Key)
}

func (n Notary) run(ctx context.Context) (*NotaryReport, error) {
	if n.Store == nil || n.Signer == nil {
		return nil, errors.New("notary: an archive and a signer are required")
	}
	if n.Settle < 0 {
		return nil, errors.New("notary: the settle window is not negative")
	}
	public, err := n.Signer.PublicKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("notary: the signer: %w", err)
	}
	pub, err := keys.ParseECPublic(public)
	if err != nil {
		return nil, fmt.Errorf("notary: %w", err)
	}
	report := &NotaryReport{Key: seal.Thumbprint(pub), Ages: map[string]float64{}}
	if err := n.ensureRoots(ctx, pub); err != nil {
		return report, err
	}

	reporter, err := newReporter(n.Catalogue, n.Sink, n.Version, n.instance())
	if err != nil {
		return report, fmt.Errorf("notary: %w", err)
	}
	defer reporter.close()

	profiles := n.Profiles
	if len(profiles) == 0 {
		if profiles, err = store.Profiles(ctx, n.Store); err != nil {
			return report, fmt.Errorf("notary: %w", err)
		}
	}
	sort.Strings(profiles)

	now := n.now()
	// The last hour that can be sealed is the last whose end, and the settle
	// window after it, has passed.
	last := now.Add(-n.Settle).Truncate(time.Hour).Add(-time.Hour)

	var errs []error
	for _, profile := range profiles {
		if why := store.KeyComponent(profile); why != "" {
			return report, fmt.Errorf("notary: the profile %q %s", profile, why)
		}
		tenants, err := store.Tenants(ctx, n.Store, profile)
		if err != nil {
			return report, fmt.Errorf("notary: %w", err)
		}
		var laggard time.Time // the oldest newest-sealed hour of the profile
		written := 0
		for _, tenant := range tenants {
			newest, err := n.tenant(ctx, profile, tenant, last, report, reporter, &written)
			if err != nil {
				report.Failures = append(report.Failures, NotaryFailure{Profile: profile, Tenant: tenant, Reason: err.Error()})
				errs = append(errs, fmt.Errorf("%s/%s: %w", profile, tenant, err))
				if n.Metrics != nil {
					n.Metrics.Failed(profile)
				}
			}
			if !newest.IsZero() && (laggard.IsZero() || newest.Before(laggard)) {
				laggard = newest
			}
		}
		if !laggard.IsZero() {
			age := now.Sub(laggard.Add(time.Hour)).Seconds()
			report.Ages[profile] = age
			if n.Metrics != nil {
				n.Metrics.Age(profile, age)
			}
		}
		if n.Metrics != nil && written > 0 {
			n.Metrics.Sealed(profile, written)
		}
	}
	return report, errors.Join(errs...)
}

func (n Notary) instance() string {
	if n.Instance != "" {
		return n.Instance
	}
	return record.InstanceName()
}

// ensureRoots makes sure keys/roots.jwks lists the key this notary signs with:
// it puts the file once, with a conditional put, when the bucket has none, and
// otherwise requires the key to be in it. The file is distribution and not
// trust: it is how an auditor finds the public key, and what they believe is
// the thumbprint they pinned.
func (n Notary) ensureRoots(ctx context.Context, pub *ecdsa.PublicKey) error {
	kid := seal.Thumbprint(pub)
	body, err := n.Store.Get(ctx, store.RootsKey)
	if errors.Is(err, store.ErrNotFound) {
		jwks, merr := seal.MarshalJWKS(pub)
		if merr != nil {
			return merr
		}
		err = n.Store.Put(ctx, store.Object{
			Key: store.RootsKey, Body: append(jwks, '\n'), ContentType: "application/jwk-set+json",
			RetainUntil: n.now().Add(rootsRetention),
		})
		if err == nil {
			return nil
		}
		if !errors.Is(err, store.ErrExists) {
			return fmt.Errorf("notary: writing %s: %w", store.RootsKey, err)
		}
		// Another run put it first; read what it put.
		if body, err = n.Store.Get(ctx, store.RootsKey); err != nil {
			return fmt.Errorf("notary: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("notary: reading %s: %w", store.RootsKey, err)
	}
	set, err := seal.ParseJWKS(body)
	if err != nil {
		return fmt.Errorf("notary: %s: %w", store.RootsKey, err)
	}
	if _, ok := set[kid]; !ok {
		return fmt.Errorf("notary: %s does not list the signing key %s: "+
			"add it (the file is the operator's) before the notary signs with it", store.RootsKey, kid)
	}
	return nil
}

// tenant seals the hours of one profile's tenant that have closed and settled
// and are not sealed yet, oldest first, and returns the newest hour it is now
// sealed to. It stops at the first hour it cannot seal: the seals are a chain.
func (n Notary) tenant(
	ctx context.Context, profile, tenant string, last time.Time, report *NotaryReport, r *reporter, written *int,
) (time.Time, error) {
	var (
		newest time.Time
		next   time.Time
		prev   string
		retain time.Time
	)
	tail, found, err := store.LastSeal(ctx, n.Store, profile, tenant)
	if err != nil {
		return newest, err
	}
	if found {
		body, err := n.Store.Get(ctx, tail.Key)
		if err != nil {
			return newest, err
		}
		typed, err := seal.ParseSeal(body)
		if err != nil {
			return newest, fmt.Errorf("the newest seal, %s, cannot be read: %w", tail.Key, err)
		}
		at, _ := store.ParseSealKey(tail.Key)
		if typed.Seal.GetProfile() != profile || typed.Seal.GetTenant() != tenant ||
			!typed.Seal.GetHour().AsTime().Equal(at.Hour) {
			return newest, fmt.Errorf("the newest seal, %s, says it covers something else; refusing to chain onto it", tail.Key)
		}
		head, err := n.Store.Head(ctx, tail.Key)
		if err != nil {
			return newest, err
		}
		prev, retain = seal.Hash(body), head.RetainUntil
		newest, next = at.Hour, at.Hour.Add(time.Hour)
	} else {
		// The first seal is of the hour of the tenant's first object; nothing
		// before it is an hour of this tenant's.
		first, err := n.Store.List(ctx, store.TenantPrefix(profile, tenant), "", 1)
		if err != nil {
			return newest, err
		}
		if len(first) == 0 {
			return newest, nil
		}
		parsed, ok := store.ParseRecordKey(first[0].Key)
		if !ok {
			return newest, fmt.Errorf("%s is not a record object key: not sealing a tenant whose first object is not one", first[0].Key)
		}
		next = parsed.Hour
	}

	for hour := next; !hour.After(last); hour = hour.Add(time.Hour) {
		computed, err := seal.Compute(ctx, n.Store, profile, tenant, hour, true)
		if err != nil {
			return newest, err
		}
		if len(computed.Problems) > 0 {
			return newest, fmt.Errorf("hour %s is not sealed: %d problem(s) in its objects, the first: %s",
				hour.Format(time.RFC3339), len(computed.Problems), computed.Problems[0])
		}
		// A seal is locked as long as the records it covers. A quiet hour has
		// none, so it takes the retention of the seal before it.
		until := computed.Retain
		if until.IsZero() {
			until = retain
		}
		statement := seal.NewSeal(profile, tenant, hour, computed, prev, n.now())
		payload, err := seal.Marshal(statement)
		if err != nil {
			return newest, err
		}
		compact, err := seal.Sign(ctx, n.Signer, seal.TypSeal, payload)
		if err != nil {
			return newest, fmt.Errorf("hour %s: %w", hour.Format(time.RFC3339), err)
		}
		key := store.SealKey(profile, tenant, hour)
		err = n.Store.Put(ctx, store.Object{
			Key: key, Body: compact, RetainUntil: until, ContentType: "application/jose",
			Metadata: map[string]string{store.MetaFormat: store.Format, store.MetaSHA256: seal.Hash(compact)},
		})
		switch {
		case errors.Is(err, store.ErrExists):
			// Another run sealed the hour: take its seal as the one the chain
			// goes through, and say nothing was written.
			if compact, err = n.Store.Get(ctx, key); err != nil {
				return newest, err
			}
			report.Present++
		case err != nil:
			return newest, fmt.Errorf("hour %s: %w", hour.Format(time.RFC3339), err)
		default:
			report.Sealed = append(report.Sealed, SealedHour{
				Key: key, Profile: profile, Tenant: tenant, Hour: hour,
				Objects: computed.Objects, Records: computed.Count, Root: computed.RootHex(),
			})
			*written++
			n.written(ctx, r, key, hour, computed)
		}
		prev, retain, newest = seal.Hash(compact), until, hour
	}
	return newest, nil
}

// written records one seal in the trail, as the notary's account of itself.
func (n Notary) written(ctx context.Context, r *reporter, key string, hour time.Time, h *seal.Hour) {
	if r == nil {
		return
	}
	data, err := structpb.NewStruct(map[string]any{
		"objects": float64(h.Objects), "window_start": hour.Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	rec := succeeded(r.event("audit.seal.written", "seal", key), auditv1.Operation_OPERATION_CREATE)
	rec.Data = data
	r.record(ctx, rec)
}
