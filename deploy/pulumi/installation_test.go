package auditpulumi_test

import (
	"strings"
	"testing"

	auditpulumi "github.com/truvity/audit/deploy/pulumi"
)

func TestInstallationComponentsAreTheChartsServiceAccounts(t *testing.T) {
	got := map[string]string{}
	for _, c := range auditpulumi.InstallationComponents("audit") {
		got[c.Name] = c.ServiceAccount
	}

	want := map[string]string{"writer": "audit", "digest": "audit-digest", "verify": "audit-verify", "query": "audit-query"}
	if len(got) != len(want) {
		t.Fatalf("components %v, want %v", got, want)
	}

	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: service account %q, want %q", k, got[k], v)
		}
	}

	if r := auditpulumi.ComponentRoleName("kernel", "digest"); r != "kernel-audit-digest" {
		t.Errorf("role name %q", r)
	}
}

func TestChecks(t *testing.T) {
	for name, err := range map[string]error{
		"name":          auditpulumi.CheckName("Bad"),
		"path":          auditpulumi.CheckRolePath("audit"),
		"lock":          auditpulumi.CheckLockMode("none"),
		"profile":       auditpulumi.CheckProfile("a/b"),
		"prefix":        auditpulumi.CheckBucketPrefix("X"),
		"long prefix":   auditpulumi.CheckBucketPrefix(strings.Repeat("a", 40)),
		"urls":          auditpulumi.CheckTelemetryURLs("http://x", "https://ok"),
		"layer arch":    auditpulumi.CheckExtensionLayer([]string{"amd64"}, []string{"provided.al2023"}),
		"layer runtime": auditpulumi.CheckExtensionLayer([]string{"arm64"}, []string{"python3.12"}),
	} {
		if err == nil {
			t.Errorf("%s: want an error", name)
		}
	}

	for name, err := range map[string]error{
		"name":    auditpulumi.CheckName("audit"),
		"path":    auditpulumi.CheckRolePath("/audit/"),
		"lock":    auditpulumi.CheckLockMode("NONE"),
		"profile": auditpulumi.CheckProfile("security"),
		"prefix":  auditpulumi.CheckBucketPrefix("truvity-access-audit"),
		"urls":    auditpulumi.CheckTelemetryURLs("https://i", "https://o"),
		"layer":   auditpulumi.CheckExtensionLayer([]string{"arm64", "amd64"}, []string{"provided.al2023"}),
	} {
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
