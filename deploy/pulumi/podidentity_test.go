package auditpulumi_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	auditpulumi "github.com/truvity/audit/deploy/pulumi"
)

var clusterArn = arnp + "eks:eu-west-1:" + account + ":cluster/acme"

func podIdentity(ns, sa string) auditpulumi.PodIdentityArgs {
	return auditpulumi.PodIdentityArgs{
		ClusterName: pulumi.String("acme"), ClusterArn: pulumi.String(clusterArn), Namespace: ns, ServiceAccount: sa,
	}
}

// podTrust is the one Pod Identity statement of a role's trust policy.
func podTrust(t *testing.T, rec *recorder, role string) map[string]any {
	t.Helper()
	var d struct{ Statement []map[string]any }
	if err := json.Unmarshal([]byte(prop(rec.one(t, "aws:iam/role:Role", role), "assumeRolePolicy").StringValue()), &d); err != nil {
		t.Fatal(err)
	}
	for _, s := range d.Statement {
		if p, ok := s["Principal"].(map[string]any); ok && p["Service"] == "pods.eks.amazonaws.com" {
			return s
		}
	}
	t.Fatalf("%s: no Pod Identity statement in %+v", role, d.Statement)
	return nil
}

func trustCount(t *testing.T, rec *recorder, role string) int {
	t.Helper()
	var d struct{ Statement []map[string]any }
	if err := json.Unmarshal([]byte(prop(rec.one(t, "aws:iam/role:Role", role), "assumeRolePolicy").StringValue()), &d); err != nil {
		t.Fatal(err)
	}
	return len(d.Statement)
}

func checkPodTrust(t *testing.T, s map[string]any, ns, sa string) {
	t.Helper()
	if got := strings.Join(strs(s["Action"]), ","); got != "sts:AssumeRole,sts:TagSession" {
		t.Errorf("actions = %s", got)
	}
	c := s["Condition"].(map[string]any)
	eq := c["StringEquals"].(map[string]any)
	if eq["aws:SourceAccount"] != account || eq["aws:RequestTag/kubernetes-namespace"] != ns ||
		eq["aws:RequestTag/kubernetes-service-account"] != sa || len(eq) != 3 {
		t.Errorf("StringEquals = %+v", eq)
	}
	if c["ArnEquals"].(map[string]any)["aws:SourceArn"] != clusterArn || len(c) != 2 {
		t.Errorf("conditions = %+v", c)
	}
}

func checkAssociation(t *testing.T, rec *recorder, name, ns, sa, roleArn string) {
	t.Helper()
	d := rec.one(t, "aws:eks/podIdentityAssociation:PodIdentityAssociation", name)
	if prop(d, "clusterName").StringValue() != "acme" || prop(d, "namespace").StringValue() != ns ||
		prop(d, "serviceAccount").StringValue() != sa {
		t.Errorf("association %s: %+v", name, d.Inputs)
	}
	if got := prop(d, "roleArn").StringValue(); got == "" || !strings.HasSuffix(roleArn, got[strings.LastIndex(got, "/")+1:]) {
		t.Errorf("association %s role = %q, want %s", name, got, roleArn)
	}
}

func TestTheObserveRoleCanTrustEKSPodIdentity(t *testing.T) {
	rec, out, err := build(t, func(a *auditpulumi.Args) {
		pi := podIdentity("audit", "audit-observe")
		pi.PermissionsBoundaryArn = arnp + "iam::" + account + ":policy/boundary"
		pi.Region = "eu-west-1"
		a.Observe = &auditpulumi.ObserveArgs{PodIdentity: &pi}
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := trustCount(t, rec, "audit-observe-reader"); n != 1 {
		t.Fatalf("statements = %d", n)
	}
	checkPodTrust(t, podTrust(t, rec, "audit-observe-reader"), "audit", "audit-observe")
	role := rec.one(t, "aws:iam/role:Role", "audit-observe-reader")
	if got := prop(role, "permissionsBoundary").StringValue(); got != arnp+"iam::"+account+":policy/boundary" {
		t.Errorf("boundary = %q", got)
	}
	checkAssociation(t, rec, "audit-observe-pia", "audit", "audit-observe", out["observeRole"])
	if prop(rec.one(t, "aws:eks/podIdentityAssociation:PodIdentityAssociation", "audit-observe-pia"), "region").StringValue() != "eu-west-1" {
		t.Error("region not set on the association")
	}
}

func TestObservePodIdentityMayBeCombinedWithAPrincipalButNotWithIRSA(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) {
		pi := podIdentity("audit", "audit-observe")
		a.Observe.PodIdentity = &pi
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := trustCount(t, rec, "audit-observe-reader"); n != 2 {
		t.Errorf("statements = %d", n)
	}
	if prop(rec.one(t, "aws:iam/role:Role", "audit-observe-reader"), "permissionsBoundary").IsString() {
		t.Error("a boundary without being asked")
	}
	_, _, err = build(t, func(a *auditpulumi.Args) {
		pi := podIdentity("audit", "audit-observe")
		a.Observe = &auditpulumi.ObserveArgs{IRSA: irsa("audit", "audit-observe"), PodIdentity: &pi}
	})
	if err == nil || !strings.Contains(err.Error(), "alternatives") {
		t.Errorf("err = %v", err)
	}
}

func TestTheQueryRoleReadsTheArchiveAndSendsReadsToTheQueue(t *testing.T) {
	rec, out, err := build(t, func(a *auditpulumi.Args) {
		a.Query = &auditpulumi.QueryArgs{PodIdentity: podIdentity("audit", "audit-query"), RecordReads: true}
	})
	if err != nil {
		t.Fatal(err)
	}
	checkPodTrust(t, podTrust(t, rec, "audit-query"), "audit", "audit-query")
	if out["queryRole"] != arnp+"iam::"+account+":role/audit/audit-query" {
		t.Errorf("queryRole = %q", out["queryRole"])
	}
	checkAssociation(t, rec, "audit-query-pia", "audit", "audit-query", out["queryRole"])
	g := grants(policy(t, rec, "audit-query"))
	for _, p := range []string{"/records/*", "/catalogue/*", "/schema/*", "/seals/*", "/keys/*"} {
		if !hasResource(g, "s3:GetObject", p) {
			t.Errorf("cannot read %s", p)
		}
	}
	if len(g["sqs:SendMessage"]) != 1 || g["sqs:SendMessage"][0] != out["queueArn"] {
		t.Errorf("sqs:SendMessage = %v, want the ingest queue", g["sqs:SendMessage"])
	}
	if _, ok := g["kms:Decrypt"]; !ok {
		t.Error("no kms:Decrypt")
	}
	for a := range g {
		if strings.HasPrefix(a, "s3:Put") || strings.HasPrefix(a, "s3:Delete") || a == "kms:Sign" || a == "sqs:ReceiveMessage" {
			t.Errorf("may %s", a)
		}
	}
}

func TestTheQueryRoleSendsNothingWithoutRecordReads(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) {
		a.Query = &auditpulumi.QueryArgs{PodIdentity: podIdentity("audit", "audit-query")}
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := grants(policy(t, rec, "audit-query"))["sqs:SendMessage"]; ok {
		t.Error("sqs:SendMessage without RecordReads")
	}
}

func TestNoQueryRoleWithoutQuery(t *testing.T) {
	rec, out, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["queryRole"] != "" || len(rec.ofType("aws:eks/podIdentityAssociation:PodIdentityAssociation")) != 0 {
		t.Errorf("a query role or association without Query: %q", out["queryRole"])
	}
}

func TestPodIdentityOptionsThatCannotWorkAreRefused(t *testing.T) {
	q := func(f func(*auditpulumi.PodIdentityArgs)) func(*auditpulumi.Args) {
		return func(a *auditpulumi.Args) {
			pi := podIdentity("audit", "audit-query")
			f(&pi)
			a.Query = &auditpulumi.QueryArgs{PodIdentity: pi}
		}
	}
	for name, c := range map[string]struct {
		edit func(*auditpulumi.Args)
		says string
	}{
		"no cluster name":         {q(func(p *auditpulumi.PodIdentityArgs) { p.ClusterName = nil }), "ClusterName"},
		"no cluster arn":          {q(func(p *auditpulumi.PodIdentityArgs) { p.ClusterArn = nil }), "ClusterArn"},
		"no namespace":            {q(func(p *auditpulumi.PodIdentityArgs) { p.Namespace = "" }), "Namespace"},
		"no account":              {q(func(p *auditpulumi.PodIdentityArgs) { p.ServiceAccount = "" }), "ServiceAccount"},
		"a wildcard":              {q(func(p *auditpulumi.PodIdentityArgs) { p.ServiceAccount = "audit-*" }), "DNS-1123"},
		"a policy variable":       {q(func(p *auditpulumi.PodIdentityArgs) { p.ServiceAccount = "${aws:username}" }), "DNS-1123"},
		"an upper-case namespace": {q(func(p *auditpulumi.PodIdentityArgs) { p.Namespace = "Audit" }), "Namespace"},
		"a long namespace":        {q(func(p *auditpulumi.PodIdentityArgs) { p.Namespace = strings.Repeat("a", 64) }), "63"},
		"a dotted namespace":      {q(func(p *auditpulumi.PodIdentityArgs) { p.Namespace = "a.b" }), "Namespace"},
		"a long service account":  {q(func(p *auditpulumi.PodIdentityArgs) { p.ServiceAccount = strings.Repeat("a", 254) }), "253"},
		"one service account twice": {func(a *auditpulumi.Args) {
			pi := podIdentity("audit", "same")
			a.Observe = &auditpulumi.ObserveArgs{PodIdentity: &pi}
			a.Query = &auditpulumi.QueryArgs{PodIdentity: podIdentity("audit", "same")}
		}, "one association per ServiceAccount"},
		"record reads, no queue": {func(a *auditpulumi.Args) {
			a.Ingest.Disabled = true
			a.Query = &auditpulumi.QueryArgs{PodIdentity: podIdentity("audit", "q"), RecordReads: true}
		}, "Ingest.Disabled"},
		"observe, no cluster": {func(a *auditpulumi.Args) {
			pi := podIdentity("audit", "o")
			pi.ClusterArn = nil
			a.Observe = &auditpulumi.ObserveArgs{PodIdentity: &pi}
		}, "Observe.PodIdentity.ClusterArn"},
	} {
		t.Run(name, func(t *testing.T) {
			rec, _, err := build(t, c.edit)
			if err == nil || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("got %v, want a refusal naming %q", err, c.says)
			}
			if len(rec.ofType("aws:iam/role:Role")) != 0 {
				t.Error("resources were declared before the arguments were refused")
			}
		})
	}
}

func TestAClusterArnThatIsNotAnEKSClusterIsRefused(t *testing.T) {
	_, err := auditpulumi.PodIdentityTrust(arnp+"iam::"+account+":role/x", "audit", "audit-query")
	if err == nil || !strings.Contains(err.Error(), "ARN of an EKS cluster") {
		t.Errorf("err = %v", err)
	}
	if _, err := auditpulumi.PodIdentityTrust(clusterArn, "audit", "audit-query"); err != nil {
		t.Error(err)
	}
}
