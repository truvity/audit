package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"sigs.k8s.io/yaml"

	"github.com/truvity/audit/auth"
	"github.com/truvity/audit/store"
	"github.com/truvity/audit/store/s3store"
)

// GrantsFile is what a deployment writes to say who may read what.
//
// It is a file rather than a call to a policy engine because a mapping an
// auditor can read is worth more here than one a service can compute, and
// because the engine is a dependency a deployment should choose rather than
// inherit.
type GrantsFile struct {
	// Issuers are the token issuers trusted to say who a caller is. They live
	// in this file, beside the rules, because a rule is only as safe as the
	// issuers able to satisfy it; see auth.Rule.Issuer.
	Issuers []IssuerEntry `json:"issuers,omitempty"`
	Rules   []GrantRule   `json:"rules"`
}

// IssuerEntry is one trusted issuer.
type IssuerEntry struct {
	URL      string `json:"url"`
	Audience string `json:"audience"`
}

// GrantRule maps a claim value to a grant.
type GrantRule struct {
	Name   string `json:"name"`
	Issuer string `json:"issuer,omitempty"`
	Claim  string `json:"claim,omitempty"`
	Value  string `json:"value,omitempty"`
	Grant  struct {
		AllTenants bool     `json:"all_tenants,omitempty"`
		Tenants    []string `json:"tenants,omitempty"`
		Profiles   []string `json:"profiles"`
		Operations []string `json:"operations"`
	} `json:"grant"`
}

// Access is a grants file read and checked: who may authenticate, and what each
// of them may see.
type Access struct {
	Issuers []auth.Issuer
	Rules   auth.Declarative
}

// LoadAccess reads a grants file with its issuers, and refuses a rule set that
// would let one issuer satisfy a rule meant for another.
func LoadAccess(path string) (Access, error) {
	file, err := readGrants(path)
	if err != nil {
		return Access{}, err
	}
	rules, err := file.rules(path)
	if err != nil {
		return Access{}, err
	}
	out := Access{Rules: rules}
	names := make([]string, 0, len(file.Issuers))
	for _, is := range file.Issuers {
		out.Issuers = append(out.Issuers, auth.Issuer{URL: is.URL, Audience: is.Audience})
		names = append(names, is.URL)
	}
	if err := rules.BoundTo(names); err != nil {
		return Access{}, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

// operations are the names a grant may carry, so that a misspelt one is an
// error at start-up rather than a grant that silently allows nothing.
var operations = map[auth.Operation]bool{
	auth.Search: true, auth.Facets: true, auth.Get: true,
	auth.Export: true, auth.Tail: true, auth.Resolve: true,
}

// LoadGrants reads the mapping. An empty path is not a default-allow: it is a
// deployment that has granted nobody anything, and every request is denied.
func LoadGrants(path string) (auth.Declarative, error) {
	file, err := readGrants(path)
	if err != nil {
		return auth.Declarative{}, err
	}
	return file.rules(path)
}

// readGrants parses a grants file; an empty path is an empty file.
func readGrants(path string) (GrantsFile, error) {
	var file GrantsFile
	if path == "" {
		return file, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return file, err
	}
	if err := yaml.UnmarshalStrict(raw, &file); err != nil {
		return file, fmt.Errorf("%s: %w", path, err)
	}
	return file, nil
}

// rules checks and converts a file's rules.
func (file GrantsFile) rules(path string) (auth.Declarative, error) {
	out := auth.Declarative{}
	for _, r := range file.Rules {
		if len(r.Grant.Profiles) == 0 || len(r.Grant.Operations) == 0 {
			return auth.Declarative{}, fmt.Errorf(
				"%s: rule %q grants no profile or no operation, which grants nothing; "+
					"remove it or say what it is for", path, r.Name)
		}
		if !r.Grant.AllTenants && len(r.Grant.Tenants) == 0 {
			return auth.Declarative{}, fmt.Errorf(
				"%s: rule %q names no tenant; say `all_tenants: true` if that is what is meant",
				path, r.Name)
		}
		ops := make([]auth.Operation, 0, len(r.Grant.Operations))
		for _, o := range r.Grant.Operations {
			if !operations[auth.Operation(o)] {
				return auth.Declarative{}, fmt.Errorf(
					"%s: rule %q grants %q, which is not an operation", path, r.Name, o)
			}
			ops = append(ops, auth.Operation(o))
		}
		out.Rules = append(out.Rules, auth.Rule{
			Name: r.Name, Issuer: r.Issuer, Claim: r.Claim, Value: r.Value,
			Grant: auth.Grant{
				AllTenants: r.Grant.AllTenants, Tenants: r.Grant.Tenants,
				Profiles: r.Grant.Profiles, Operations: ops,
			},
		})
	}
	return out, nil
}

// Archive opens the archive for a command that only reads it.
func Archive(ctx context.Context, bucket, prefix, region string) (store.Store, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	if region != "" {
		cfg.Region = region
	}
	return s3store.FromConfig(cfg, s3store.Options{Bucket: bucket, Prefix: prefix})
}

// ExportStore opens the bucket exports are written to, without Object Lock.
//
// It is a different function from Archive on purpose: an export is a copy of
// records made to be taken away and then cleared, and a lock would keep it. A
// bucket without Object Lock refuses a put that names a lock mode, so this is
// also the only way to write to one.
func ExportStore(ctx context.Context, bucket, region string) (store.Store, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	if region != "" {
		cfg.Region = region
	}
	return s3store.FromConfig(cfg, s3store.Options{Bucket: bucket, Unlocked: true})
}
