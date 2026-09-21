package cli

import (
	"fmt"
	"os"

	"sigs.k8s.io/yaml"

	"github.com/truvity/audit/auth"
	"github.com/truvity/audit/sink"
)

// WorkloadsFile says which issuers the writer and the registry trust to name a
// calling workload, and which source each workload speaks for.
//
// In a cluster the issuer is the cluster's own OIDC issuer and the subjects are
// service accounts: a workload presents its projected token, and the kubelet —
// not the workload — decides whose it is.
type WorkloadsFile struct {
	Issuers   []IssuerEntry   `json:"issuers"`
	Workloads []WorkloadEntry `json:"workloads,omitempty"`
}

// WorkloadEntry is one service account and the source it speaks for.
type WorkloadEntry struct {
	Issuer  string `json:"issuer,omitempty"`
	Subject string `json:"subject"`
	Source  string `json:"source"`
}

// Workloads is a workloads file read and checked.
type Workloads struct {
	Issuers []auth.Issuer
	Map     auth.Workloads
}

// LoadWorkloads reads and checks a workloads file. It refuses one with no
// issuer, because a service configured with it could never admit anybody.
func LoadWorkloads(path string) (Workloads, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Workloads{}, err
	}
	var file WorkloadsFile
	if err := yaml.UnmarshalStrict(raw, &file); err != nil {
		return Workloads{}, fmt.Errorf("%s: %w", path, err)
	}
	if len(file.Issuers) == 0 {
		return Workloads{}, fmt.Errorf("%s: names no issuer, so no workload could ever be verified", path)
	}
	var out Workloads
	names := make([]string, 0, len(file.Issuers))
	for _, is := range file.Issuers {
		out.Issuers = append(out.Issuers, auth.Issuer{URL: is.URL, Audience: is.Audience})
		names = append(names, is.URL)
	}
	for _, w := range file.Workloads {
		out.Map = append(out.Map, auth.Workload{Issuer: w.Issuer, Subject: w.Subject, Source: w.Source})
	}
	if err := out.Map.BoundTo(names); err != nil {
		return Workloads{}, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

// TokenFileEnv names the file holding the token a process presents to the
// writer and the registry: in a cluster, its projected service-account token.
const TokenFileEnv = "AUDIT_TOKEN_FILE"

// WriterClient is a client to the writer at url, presenting the token named by
// AUDIT_TOKEN_FILE when it is set. Every command that records through the
// writer builds its client here, so that none of them is the one that forgot.
func WriterClient(url string) *sink.Client {
	if path := os.Getenv(TokenFileEnv); path != "" {
		return sink.NewClient(auth.TokenFile(path), url)
	}
	return sink.NewClient(nil, url)
}
