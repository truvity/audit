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
	Rules []GrantRule `json:"rules"`
}

// GrantRule maps a claim value to a grant.
type GrantRule struct {
	Name  string `json:"name"`
	Claim string `json:"claim,omitempty"`
	Value string `json:"value,omitempty"`
	Grant struct {
		AllTenants bool     `json:"all_tenants,omitempty"`
		Tenants    []string `json:"tenants,omitempty"`
		Profiles   []string `json:"profiles"`
		Operations []string `json:"operations"`
	} `json:"grant"`
}

// LoadGrants reads the mapping. An empty path is not a default-allow: it is a
// deployment that has granted nobody anything, and every request is denied.
func LoadGrants(path string) (auth.Declarative, error) {
	if path == "" {
		return auth.Declarative{}, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return auth.Declarative{}, err
	}
	var file GrantsFile
	if err := yaml.UnmarshalStrict(raw, &file); err != nil {
		return auth.Declarative{}, fmt.Errorf("%s: %w", path, err)
	}

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
			ops = append(ops, auth.Operation(o))
		}
		out.Rules = append(out.Rules, auth.Rule{
			Name: r.Name, Claim: r.Claim, Value: r.Value,
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
