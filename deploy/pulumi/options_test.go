package auditpulumi_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	yaml "go.yaml.in/yaml/v3"

	policyconfig "github.com/truvity/policy/config"

	auditpulumi "github.com/truvity/audit/deploy/pulumi"
)

const (
	callerIdentity = "aws:index/getCallerIdentity:getCallerIdentity"
	issuerHost     = "k8s.example.test"
)

var oidcArn = arnp + "iam::" + account + ":oidc-provider/" + issuerHost

func irsa(ns, sa string) *auditpulumi.IRSAArgs {
	return &auditpulumi.IRSAArgs{
		OIDCProviderArn: pulumi.String(oidcArn), IssuerHost: issuerHost, Namespace: ns, ServiceAccount: sa,
	}
}

func (r *recorder) invokes(token string) []mockCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []mockCall
	for _, c := range r.calls {
		if c.Token == token {
			out = append(out, c)
		}
	}
	return out
}

// ---- the provider

func TestTheCallersProviderIsUsedForTheAccountLookup(t *testing.T) {
	rec, _, err := buildWith(t, nil, func(ctx *pulumi.Context) ([]pulumi.ResourceOption, error) {
		p, err := aws.NewProvider(ctx, "hive", &aws.ProviderArgs{Region: pulumi.String("eu-west-1")})
		if err != nil {
			return nil, err
		}
		return []pulumi.ResourceOption{pulumi.Provider(p)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := rec.invokes(callerIdentity)
	if len(calls) != 1 {
		t.Fatalf("%d account lookups, want 1: %v", len(calls), rec.calls)
	}
	if calls[0].Provider == "" {
		t.Error("the lookup went through the default provider, which a stack may have disabled")
	}
	if !strings.Contains(calls[0].Provider, "pulumi:providers:aws::hive") {
		t.Errorf("the lookup went through %q, not the caller's provider", calls[0].Provider)
	}
}

func TestTheProvidersOptionIsUsedToo(t *testing.T) {
	rec, _, err := buildWith(t, nil, func(ctx *pulumi.Context) ([]pulumi.ResourceOption, error) {
		p, err := aws.NewProvider(ctx, "hive", &aws.ProviderArgs{Region: pulumi.String("eu-west-1")})
		if err != nil {
			return nil, err
		}
		return []pulumi.ResourceOption{pulumi.Providers(p)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if c := rec.invokes(callerIdentity); len(c) != 1 || !strings.Contains(c[0].Provider, "pulumi:providers:aws::hive") {
		t.Errorf("lookups: %v", c)
	}
}

func TestAnAccountIDSkipsTheLookupAndIsInTheSealKeyPolicy(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) { a.AccountID = otherAccount })
	if err != nil {
		t.Fatal(err)
	}
	if c := rec.invokes(callerIdentity); len(c) != 0 {
		t.Errorf("an invoke was made although the account was given: %v", c)
	}
	pol := prop(rec.one(t, "aws:kms/key:Key", "audit-seal"), "policy").StringValue()
	if !strings.Contains(pol, arnp+"iam::"+otherAccount+":root") {
		t.Errorf("the seal key's policy does not name the given account: %s", pol)
	}
}

func TestNoLookupIsMadeWithoutANotary(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) { a.Notary.Disabled = true })
	if err != nil {
		t.Fatal(err)
	}
	if c := rec.invokes(callerIdentity); len(c) != 0 {
		t.Errorf("an invoke was made with nothing to use it for: %v", c)
	}
}

// ---- SSE-S3

func sseOf(t *testing.T, rec *recorder) map[string]resourceValue {
	t.Helper()
	sse := rec.one(t, "aws:s3/bucketServerSideEncryptionConfiguration:BucketServerSideEncryptionConfiguration", "audit-archive")
	rule := prop(sse, "rules").ArrayValue()[0].ObjectValue()
	return map[string]resourceValue{
		"alg":    {rule["applyServerSideEncryptionByDefault"].ObjectValue()["sseAlgorithm"].StringValue()},
		"hasKey": {boolStr(rule["applyServerSideEncryptionByDefault"].ObjectValue()["kmsMasterKeyId"].V != nil)},
	}
}

type resourceValue struct{ S string }

func boolStr(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func TestTheDefaultEncryptionIsStillKMSUnderTheArchiveKey(t *testing.T) {
	rec, out, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := sseOf(t, rec)
	if got["alg"].S != "aws:kms" || got["hasKey"].S != "yes" || out["archiveKeyArn"] == "" {
		t.Errorf("default encryption: %v, key %q", got, out["archiveKeyArn"])
	}
	// The explicit spelling is the same.
	rec2, _, err := build(t, func(a *auditpulumi.Args) { a.Archive.Encryption = auditpulumi.EncryptionKMS })
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.names()) != len(rec2.names()) {
		t.Errorf("Encryption %q changed what is created", auditpulumi.EncryptionKMS)
	}
}

func TestSSES3CreatesNoArchiveKeyAndNoRoleMayUseOne(t *testing.T) {
	rec, out, err := build(t, func(a *auditpulumi.Args) {
		a.Archive.Encryption = auditpulumi.EncryptionS3
		a.Observe.IRSA = irsa("audit", "observe")
		a.ArchiveWriter = &auditpulumi.ArchiveWriterArgs{IRSA: *irsa("audit", "digest")}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := sseOf(t, rec); got["alg"].S != "AES256" || got["hasKey"].S != "no" {
		t.Errorf("encryption: %v", got)
	}
	for _, d := range rec.ofType("aws:kms/key:Key") {
		if d.Name != "audit-seal" {
			t.Errorf("a KMS key %s with SSE-S3", d.Name)
		}
	}
	for _, d := range rec.ofType("aws:kms/alias:Alias") {
		if d.Name != "audit-seal" {
			t.Errorf("a KMS alias %s with SSE-S3", d.Name)
		}
	}
	if out["archiveKeyArn"] != "" {
		t.Errorf("archiveKeyArn = %q", out["archiveKeyArn"])
	}
	for _, role := range []string{"audit-writer", "audit-notary", "audit-observe-reader", "audit-archive-writer"} {
		g := grants(policy(t, rec, role))
		for _, a := range []string{"kms:GenerateDataKey", "kms:Decrypt"} {
			if _, ok := g[a]; ok {
				t.Errorf("%s may %s with no archive key", role, a)
			}
		}
		if _, ok := g["s3:ListBucket"]; !ok {
			t.Errorf("%s lost its S3 rights", role)
		}
	}
	// The seal key is a different key: the notary still signs with it.
	if g := grants(policy(t, rec, "audit-notary")); len(g["kms:Sign"]) != 1 || g["kms:Sign"][0] != out["sealKeyArn"] {
		t.Errorf("the notary's Sign: %v", g["kms:Sign"])
	}
	for _, fn := range []string{"audit-writer", "audit-notary"} {
		body := layerFiles(t, rec, fn)["audit.yaml"]
		if strings.Contains(body, "kmsKey") {
			t.Errorf("%s names an archive key with SSE-S3:\n%s", fn, body)
		}
	}
}

func TestTheSSES3ConfigurationsValidateAgainstTheBinariesSchemas(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) { a.Archive.Encryption = auditpulumi.EncryptionS3 })
	if err != nil {
		t.Fatal(err)
	}
	validateConfigs(t, rec, map[string]string{"audit-writer": "audit-writer-lambda", "audit-notary": "audit-notary"})
}

func validateConfigs(t *testing.T, rec *recorder, fns map[string]string) {
	t.Helper()
	for fn, schema := range fns {
		body := layerFiles(t, rec, fn)["audit.yaml"]
		var doc any
		if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join("..", "..", "schemas", "config", schema+".schema.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := policyconfig.Validate(doc, raw); err != nil {
			t.Errorf("%s does not validate against %s: %v\n%s", fn, schema, err, body)
		}
	}
}

// ---- IRSA

type trustDoc struct {
	Statement []struct {
		Effect    string
		Principal map[string]string
		Action    string
		Condition map[string]map[string]string
	}
}

func trustOf(t *testing.T, rec *recorder, role string) trustDoc {
	t.Helper()
	var d trustDoc
	if err := json.Unmarshal([]byte(prop(rec.one(t, "aws:iam/role:Role", role), "assumeRolePolicy").StringValue()), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestTheObserveRoleCanTrustAnOIDCProviderAndNothingElse(t *testing.T) {
	rec, out, err := build(t, func(a *auditpulumi.Args) {
		a.Observe = &auditpulumi.ObserveArgs{IRSA: irsa("audit", "audit-observe")}
	})
	if err != nil {
		t.Fatal(err)
	}
	tr := trustOf(t, rec, "audit-observe-reader")
	if len(tr.Statement) != 1 {
		t.Fatalf("trust: %+v", tr)
	}
	s := tr.Statement[0]
	if s.Effect != "Allow" || s.Action != "sts:AssumeRoleWithWebIdentity" || s.Principal["Federated"] != oidcArn || len(s.Principal) != 1 {
		t.Errorf("statement: %+v", s)
	}
	eq := s.Condition["StringEquals"]
	if len(s.Condition) != 1 || len(eq) != 2 ||
		eq[issuerHost+":sub"] != "system:serviceaccount:audit:audit-observe" || eq[issuerHost+":aud"] != "sts.amazonaws.com" {
		t.Errorf("conditions: %+v", s.Condition)
	}
	if out["observeRole"] != arnp+"iam::"+account+":role/audit/audit-observe-reader" {
		t.Errorf("observeRole = %q", out["observeRole"])
	}
	// The read-only role: the five prefixes, a list, a decrypt, no writes.
	g := grants(policy(t, rec, "audit-observe-reader"))
	for _, p := range []string{"/records/*", "/catalogue/*", "/schema/*", "/seals/*", "/keys/*"} {
		if !hasResource(g, "s3:GetObject", p) {
			t.Errorf("cannot read %s", p)
		}
	}
	for a := range g {
		if strings.HasPrefix(a, "s3:Put") || strings.HasPrefix(a, "s3:Delete") || strings.HasPrefix(a, "kms:Sign") {
			t.Errorf("may %s", a)
		}
	}
	if _, ok := g["s3:ListBucket"]; !ok {
		t.Error("no ListBucket")
	}
}

func TestTheAudienceOfAnIRSATrustIsAParameter(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) {
		i := irsa("audit", "audit-observe")
		i.Audience = "audit.example.test"
		a.Observe = &auditpulumi.ObserveArgs{IRSA: i}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := trustOf(t, rec, "audit-observe-reader").Statement[0].Condition["StringEquals"][issuerHost+":aud"]; got != "audit.example.test" {
		t.Errorf("aud = %q", got)
	}
}

func TestTheObserveRoleMayTrustAPrincipalAndAServiceAccountAtOnce(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) { a.Observe.IRSA = irsa("audit", "audit-observe") })
	if err != nil {
		t.Fatal(err)
	}
	tr := trustOf(t, rec, "audit-observe-reader")
	var actions []string
	for _, s := range tr.Statement {
		actions = append(actions, s.Action)
	}
	sort.Strings(actions)
	if strings.Join(actions, ",") != "sts:AssumeRole,sts:AssumeRoleWithWebIdentity" {
		t.Errorf("trust: %+v", tr)
	}
}

// ---- the archive writer

func TestTheArchiveWriterRoleTrustsOneServiceAccountAndWritesSealsAndKeysOnly(t *testing.T) {
	rec, out, err := build(t, func(a *auditpulumi.Args) {
		a.ArchiveWriter = &auditpulumi.ArchiveWriterArgs{IRSA: *irsa("audit", "digest")}
	})
	if err != nil {
		t.Fatal(err)
	}
	r := rec.one(t, "aws:iam/role:Role", "audit-archive-writer")
	if prop(r, "path").StringValue() != "/audit/" {
		t.Errorf("path %s", prop(r, "path").StringValue())
	}
	s := trustOf(t, rec, "audit-archive-writer").Statement
	if len(s) != 1 || s[0].Action != "sts:AssumeRoleWithWebIdentity" || s[0].Principal["Federated"] != oidcArn ||
		s[0].Condition["StringEquals"][issuerHost+":sub"] != "system:serviceaccount:audit:digest" ||
		s[0].Condition["StringEquals"][issuerHost+":aud"] != "sts.amazonaws.com" {
		t.Errorf("trust: %+v", s)
	}
	g := grants(policy(t, rec, "audit-archive-writer"))
	for _, a := range []string{"s3:PutObject", "s3:PutObjectRetention"} {
		if len(g[a]) != 2 || !hasResource(g, a, "/seals/*") || !hasResource(g, a, "/keys/*") {
			t.Errorf("%s on %v", a, g[a])
		}
	}
	for _, p := range []string{"/records/*", "/seals/*", "/keys/*"} {
		if !hasResource(g, "s3:GetObject", p) {
			t.Errorf("cannot read %s", p)
		}
	}
	for a := range g {
		switch a {
		case "s3:PutObject", "s3:PutObjectRetention", "s3:GetObject", "s3:ListBucket", "kms:GenerateDataKey", "kms:Decrypt":
		default:
			t.Errorf("the archive writer may %s", a)
		}
	}
	if g["kms:Decrypt"][0] != out["archiveKeyArn"] {
		t.Errorf("decrypts under %v", g["kms:Decrypt"])
	}
	if out["archiveWriterRole"] != arnp+"iam::"+account+":role/audit/audit-archive-writer" {
		t.Errorf("archiveWriterRole = %q", out["archiveWriterRole"])
	}
}

func TestTheArchiveWriterPrefixesAreAParameterAndNoLockMeansNoRetention(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) {
		a.Archive.ObjectLockMode = auditpulumi.None
		a.ArchiveWriter = &auditpulumi.ArchiveWriterArgs{IRSA: *irsa("audit", "digest"), Prefixes: []string{"records/", "dlq/"}}
	})
	if err != nil {
		t.Fatal(err)
	}
	g := grants(policy(t, rec, "audit-archive-writer"))
	if len(g["s3:PutObject"]) != 2 || !hasResource(g, "s3:PutObject", "/records/*") || !hasResource(g, "s3:PutObject", "/dlq/*") {
		t.Errorf("put on %v", g["s3:PutObject"])
	}
	if _, ok := g["s3:PutObjectRetention"]; ok {
		t.Error("PutObjectRetention with no Object Lock")
	}
	if hasResource(g, "s3:PutObject", "/seals/*") {
		t.Error("seals/ was not asked for")
	}
}

func TestNoArchiveWriterRoleWithoutArchiveWriter(t *testing.T) {
	rec, out, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rec.ofType("aws:iam/role:Role") {
		if strings.Contains(r.Name, "archive-writer") {
			t.Errorf("role %s without ArchiveWriter", r.Name)
		}
	}
	if out["archiveWriterRole"] != "" {
		t.Errorf("archiveWriterRole = %q", out["archiveWriterRole"])
	}
}

// ---- optional parts

var metricAlarms = "aws:cloudwatch/metricAlarm:MetricAlarm"

func names(rec *recorder, typ string) []string {
	var out []string
	for _, d := range rec.ofType(typ) {
		out = append(out, d.Name)
	}
	sort.Strings(out)
	return out
}

func same(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	sort.Strings(want)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("%s:\n got  %v\n want %v", what, got, want)
	}
}

func TestEveryCombinationOfIngestAndNotary(t *testing.T) {
	type want struct {
		resources                              int
		roles, keys, functions, queues, alarms []string
		table, schedule, mapping, topic        bool
	}
	cases := map[string]struct {
		ingestOff, notaryOff bool
		want                 want
	}{
		"both": {false, false, want{
			resources: 44,
			roles:     []string{"audit-notary", "audit-observe-reader", "audit-scheduler", "audit-writer"},
			keys:      []string{"audit-archive", "audit-seal"}, functions: []string{"audit-notary", "audit-writer"},
			queues: []string{"audit-ingest", "audit-ingest-dlq"},
			alarms: []string{"ingest-dlq-not-empty", "ingest-oldest-message-age", "notary-errors", "notary-silent",
				"notary-throttles", "writer-errors", "writer-throttles"},
			table: true, schedule: true, mapping: true, topic: true,
		}},
		"ingest only (hive: the notary runs on Talos)": {false, true, want{
			resources: 30,
			roles:     []string{"audit-observe-reader", "audit-writer"},
			keys:      []string{"audit-archive"}, functions: []string{"audit-writer"},
			queues: []string{"audit-ingest", "audit-ingest-dlq"},
			alarms: []string{"ingest-dlq-not-empty", "ingest-oldest-message-age", "writer-errors", "writer-throttles"},
			table:  true, mapping: true, topic: true,
		}},
		"notary only": {true, false, want{
			resources: 29,
			roles:     []string{"audit-notary", "audit-observe-reader", "audit-scheduler"},
			keys:      []string{"audit-archive", "audit-seal"}, functions: []string{"audit-notary"},
			alarms:   []string{"notary-errors", "notary-silent", "notary-throttles"},
			schedule: true, topic: true,
		}},
		"the archive alone (kernel K5b)": {true, true, want{
			resources: 13,
			roles:     []string{"audit-observe-reader"},
			keys:      []string{"audit-archive"},
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rec, out, err := build(t, func(a *auditpulumi.Args) {
				// A part that is off needs none of its arguments.
				if c.ingestOff {
					a.Writer = auditpulumi.WriterArgs{}
				}
				if c.notaryOff {
					a.Notary = auditpulumi.NotaryArgs{}
				}
				a.Ingest.Disabled, a.Notary.Disabled = c.ingestOff, c.notaryOff
			})
			if err != nil {
				t.Fatal(err)
			}
			w := c.want
			if n := len(rec.names()); n != w.resources {
				t.Errorf("%d resources, want %d: %v", n, w.resources, rec.names())
			}
			// The archive is always there.
			rec.one(t, "aws:s3/bucket:Bucket", "audit-archive")
			same(t, "roles", names(rec, "aws:iam/role:Role"), w.roles...)
			same(t, "keys", names(rec, "aws:kms/key:Key"), w.keys...)
			same(t, "functions", names(rec, "aws:lambda/function:Function"), w.functions...)
			same(t, "queues", names(rec, "aws:sqs/queue:Queue"), w.queues...)
			var alarms []string
			for _, n := range names(rec, metricAlarms) {
				alarms = append(alarms, strings.TrimPrefix(n, "audit-"))
			}
			same(t, "alarms", alarms, w.alarms...)
			for typ, on := range map[string]bool{
				"aws:dynamodb/table:Table":                                       w.table,
				"aws:scheduler/schedule:Schedule":                                w.schedule,
				"aws:lambda/eventSourceMapping:EventSourceMapping":               w.mapping,
				"aws:sns/topic:Topic":                                            w.topic,
				"aws:lambda/functionEventInvokeConfig:FunctionEventInvokeConfig": w.schedule,
			} {
				if got := len(rec.ofType(typ)) == 1; got != on {
					t.Errorf("%s present = %v, want %v", typ, got, on)
				}
			}
			for k, on := range map[string]bool{
				"queueUrl": !c.ingestOff, "queueArn": !c.ingestOff, "dlqUrl": !c.ingestOff, "dlqArn": !c.ingestOff,
				"dedupe": !c.ingestOff, "writerFn": !c.ingestOff, "writerRole": !c.ingestOff,
				"sealKeyArn": !c.notaryOff, "sealKeyAlias": !c.notaryOff, "notaryFn": !c.notaryOff, "notaryRole": !c.notaryOff,
				"schedule": !c.notaryOff, "topic": w.topic,
				"bucketArn": true, "archiveKeyArn": true, "observeRole": true,
			} {
				if (out[k] != "") != on {
					t.Errorf("output %s = %q, want set = %v", k, out[k], on)
				}
			}
			// What stays is unchanged: the roles that remain keep their rights, and the
			// rights of a part that is gone are nowhere.
			if !c.ingestOff {
				if g := grants(policy(t, rec, "audit-writer")); len(g["sqs:ReceiveMessage"]) != 1 || len(g["dynamodb:PutItem"]) != 1 {
					t.Errorf("the writer's rights: %v", g)
				}
			}
			if !c.notaryOff {
				if g := grants(policy(t, rec, "audit-notary")); len(g["kms:Sign"]) != 1 {
					t.Errorf("the notary's rights: %v", g)
				}
			}
			for _, r := range rec.ofType("aws:iam/rolePolicy:RolePolicy") {
				body := prop(r, "policy").StringValue()
				if c.notaryOff && (strings.Contains(body, "kms:Sign") || strings.Contains(body, "audit-seal") || strings.Contains(body, "lambda:InvokeFunction")) {
					t.Errorf("%s still speaks of the notary: %s", r.Name, body)
				}
				if c.ingestOff && (strings.Contains(body, "sqs:") || strings.Contains(body, "dynamodb:")) {
					t.Errorf("%s still speaks of the ingest side: %s", r.Name, body)
				}
			}
		})
	}
}

func TestTheSealKeyPolicyNamesTheNotaryOnlyWhenThereIsOne(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) { a.Ingest.Disabled = true; a.Writer = auditpulumi.WriterArgs{} })
	if err != nil {
		t.Fatal(err)
	}
	if pol := prop(rec.one(t, "aws:kms/key:Key", "audit-seal"), "policy").StringValue(); !strings.Contains(pol, "audit-notary") {
		t.Errorf("seal key policy: %s", pol)
	}
}

func TestTheShippedConfigurationOfEachPartThatRemainsValidates(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) { a.Notary.Disabled = true })
	if err != nil {
		t.Fatal(err)
	}
	validateConfigs(t, rec, map[string]string{"audit-writer": "audit-writer-lambda"})
	rec, _, err = build(t, func(a *auditpulumi.Args) { a.Ingest.Disabled = true })
	if err != nil {
		t.Fatal(err)
	}
	validateConfigs(t, rec, map[string]string{"audit-notary": "audit-notary"})
}

func TestADisabledPartNeedsNoBinaryAndAnEnabledOneStillDoes(t *testing.T) {
	// No notary, no notary binary.
	if _, _, err := build(t, func(a *auditpulumi.Args) { a.Notary = auditpulumi.NotaryArgs{Disabled: true} }); err != nil {
		t.Errorf("a disabled notary needed a binary: %v", err)
	}
	// No ingest, no writer binary or deployment.
	if _, _, err := build(t, func(a *auditpulumi.Args) { a.Ingest.Disabled, a.Writer = true, auditpulumi.WriterArgs{} }); err != nil {
		t.Errorf("a disabled ingest needed a writer: %v", err)
	}
	// Both are still required when the part is on.
	for says, edit := range map[string]func(*auditpulumi.Args){
		"Writer.Package":        func(a *auditpulumi.Args) { a.Writer.Package = "" },
		"Writer.PackageSHA256":  func(a *auditpulumi.Args) { a.Writer.PackageSHA256 = "" },
		"Writer.DeploymentYAML": func(a *auditpulumi.Args) { a.Writer.DeploymentYAML = "" },
		"Notary.Package":        func(a *auditpulumi.Args) { a.Notary.Package = "" },
		"Notary.PackageSHA256":  func(a *auditpulumi.Args) { a.Notary.PackageSHA256 = "" },
	} {
		if _, _, err := build(t, edit); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("got %v, want a refusal naming %s", err, says)
		}
	}
}

func TestOptionsThatCannotWorkAreRefusedBeforeAnythingIsCreated(t *testing.T) {
	bad := func(f func(*auditpulumi.IRSAArgs)) *auditpulumi.IRSAArgs {
		i := irsa("audit", "sa")
		f(i)
		return i
	}
	for name, c := range map[string]struct {
		edit func(*auditpulumi.Args)
		says string
	}{
		"an encryption":     {func(a *auditpulumi.Args) { a.Archive.Encryption = "aes" }, "Archive.Encryption"},
		"an account":        {func(a *auditpulumi.Args) { a.AccountID = "123" }, "AccountID"},
		"observe, no trust": {func(a *auditpulumi.Args) { a.Observe = &auditpulumi.ObserveArgs{} }, "Observe.TrustedPrincipalArn or Observe.IRSA"},
		"no provider": {func(a *auditpulumi.Args) {
			a.Observe.IRSA = bad(func(i *auditpulumi.IRSAArgs) { i.OIDCProviderArn = nil })
		}, "OIDCProviderArn"},
		"no issuer": {func(a *auditpulumi.Args) { a.Observe.IRSA = bad(func(i *auditpulumi.IRSAArgs) { i.IssuerHost = "" }) }, "IssuerHost"},
		"an issuer URL": {func(a *auditpulumi.Args) {
			a.Observe.IRSA = bad(func(i *auditpulumi.IRSAArgs) { i.IssuerHost = "https://" + issuerHost })
		}, "no scheme"},
		"no namespace": {func(a *auditpulumi.Args) { a.Observe.IRSA = bad(func(i *auditpulumi.IRSAArgs) { i.Namespace = "" }) }, "Namespace"},
		"no service account": {func(a *auditpulumi.Args) {
			a.Observe.IRSA = bad(func(i *auditpulumi.IRSAArgs) { i.ServiceAccount = "" })
		}, "ServiceAccount"},
		"a wildcard": {func(a *auditpulumi.Args) {
			a.Observe.IRSA = bad(func(i *auditpulumi.IRSAArgs) { i.ServiceAccount = "*" })
		}, "not patterns"},
		"a writer, no trust": {func(a *auditpulumi.Args) { a.ArchiveWriter = &auditpulumi.ArchiveWriterArgs{} }, "ArchiveWriter.IRSA"},
		"a writer prefix": {func(a *auditpulumi.Args) {
			a.ArchiveWriter = &auditpulumi.ArchiveWriterArgs{IRSA: *irsa("a", "b"), Prefixes: []string{"holds/"}}
		}, "Prefixes"},
		"a writer prefix, 2x": {func(a *auditpulumi.Args) {
			a.ArchiveWriter = &auditpulumi.ArchiveWriterArgs{IRSA: *irsa("a", "b"), Prefixes: []string{"seals/", "seals/"}}
		}, "twice"},
	} {
		t.Run(name, func(t *testing.T) {
			rec, _, err := build(t, c.edit)
			if err == nil || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("got %v, want a refusal naming %q", err, c.says)
			}
			if len(rec.ofType("aws:s3/bucket:Bucket")) != 0 || len(rec.ofType("aws:iam/role:Role")) != 0 {
				t.Error("resources were declared before the arguments were refused")
			}
		})
	}
}

// ---- the two estates

const (
	functionType = "aws:lambda/function:Function"
	scheduleType = "aws:scheduler/schedule:Schedule"
)

// Truvity (stack `access`): both Lambdas, the notary on KMS, SSE-KMS, a lock that
// begins in GOVERNANCE, observe by IRSA from the kernel cluster.
func TestTheTruvityShapeIsExpressible(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) {
		a.Archive.ObjectLockMode = auditpulumi.Governance
		a.Observe = &auditpulumi.ObserveArgs{IRSA: irsa("audit", "audit-observe")}
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"audit-writer", "audit-notary"} {
		f := rec.one(t, functionType, fn)
		if prop(f, "vpcConfig").IsObject() {
			t.Errorf("%s is in a VPC", fn)
		}
		if !strings.Contains(layerFiles(t, rec, fn)["audit.yaml"], "lockMode: governance") {
			t.Errorf("%s is not in governance mode", fn)
		}
	}
	if n := layerFiles(t, rec, "audit-notary")["audit.yaml"]; !strings.Contains(n, "key: alias/audit-seal") {
		t.Errorf("the notary does not sign with the KMS seal key:\n%s", n)
	}
	if len(rec.ofType(scheduleType)) != 1 || len(rec.ofType(lockType)) != 1 || len(rec.ofType("aws:kms/key:Key")) != 2 {
		t.Errorf("schedules %d, locks %d, keys %d", len(rec.ofType(scheduleType)), len(rec.ofType(lockType)), len(rec.ofType("aws:kms/key:Key")))
	}
}

// hive: the writer Lambda with SSE-S3 and never a lock; the notary is a
// Kubernetes CronJob on OpenBao Transit, so AWS holds no notary, no seal key
// and no schedule, and a role for the pod to put seals/ and keys/.
func TestTheHiveShapeIsExpressible(t *testing.T) {
	rec, out, err := build(t, func(a *auditpulumi.Args) {
		a.Archive.ObjectLockMode, a.Archive.Encryption = auditpulumi.None, auditpulumi.EncryptionS3
		a.Notary = auditpulumi.NotaryArgs{Disabled: true}
		a.Telemetry = nil
		a.Observe = &auditpulumi.ObserveArgs{IRSA: irsa("audit", "audit-observe")}
		a.ArchiveWriter = &auditpulumi.ArchiveWriterArgs{IRSA: *irsa("audit", "audit-notary")}
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(rec.ofType(functionType)); n != 1 {
		t.Errorf("%d functions, want the writer alone", n)
	}
	f := rec.one(t, functionType, "audit-writer")
	if prop(f, "vpcConfig").IsObject() {
		t.Error("the writer is in a VPC")
	}
	if w := layerFiles(t, rec, "audit-writer")["audit.yaml"]; !strings.Contains(w, "lockMode: none") || strings.Contains(w, "kmsKey") {
		t.Errorf("the writer's configuration:\n%s", w)
	}
	if len(rec.ofType(scheduleType)) != 0 || len(rec.ofType(lockType)) != 0 || len(rec.ofType("aws:kms/key:Key")) != 0 {
		t.Error("a schedule, a lock or a key exists")
	}
	if out["archiveWriterRole"] == "" {
		t.Error("no archive-writer role for the Kubernetes notary")
	}
}

// ---- the application's catalogue

func writeCatalogue(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCataloguePathsAreDeliveredUnderTheirBaseNames(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) {
		a.Writer.CataloguePaths = []string{writeCatalogue(t, "catalogue-roster.yaml", "source: roster\nversion: \"1.0.0\"\n")}
		a.Writer.Catalogues = map[string]string{"catalogue.yaml": "source: app\nversion: \"1.0.0\"\n"}
	})
	if err != nil {
		t.Fatal(err)
	}
	w := layerFiles(t, rec, "audit-writer")
	if w["catalogues/catalogue-roster.yaml"] != "source: roster\nversion: \"1.0.0\"\n" || w["catalogues/catalogue.yaml"] != "source: app\nversion: \"1.0.0\"\n" {
		t.Errorf("package has %v", keys(w))
	}
	if !strings.Contains(w["audit.yaml"], "catalogues: /opt/audit/catalogues") {
		t.Errorf("the configuration does not name the directory:\n%s", w["audit.yaml"])
	}
}

// A catalogue change is a change of the configuration layer, which makes a new
// layer version and is what points the function at it: it never reaches the
// writer without a deploy.
func TestAChangedCatalogueChangesTheWritersConfigurationLayer(t *testing.T) {
	pkg := func(body string) map[string]string {
		rec, _, err := build(t, func(a *auditpulumi.Args) {
			a.Writer.CataloguePaths = []string{writeCatalogue(t, "catalogue.yaml", body)}
		})
		if err != nil {
			t.Fatal(err)
		}
		return layerFiles(t, rec, "audit-writer")
	}
	a, b := pkg("source: roster\nversion: 1\n"), pkg("source: roster\nversion: 1\nchanged: true\n")
	if a["catalogues/catalogue.yaml"] == b["catalogues/catalogue.yaml"] {
		t.Error("a changed catalogue left the package unchanged")
	}
}

func TestBadCataloguePathsAreRefused(t *testing.T) {
	empty := writeCatalogue(t, "catalogue.yaml", " \n")
	good := writeCatalogue(t, "catalogue.yaml", "a: b\n")
	for name, c := range map[string]struct {
		edit func(*auditpulumi.Args)
		want string
	}{
		"missing": {func(a *auditpulumi.Args) { a.Writer.CataloguePaths = []string{"/nonexistent/catalogue.yaml"} }, "CataloguePaths"},
		"empty":   {func(a *auditpulumi.Args) { a.Writer.CataloguePaths = []string{empty} }, "empty"},
		"badname": {func(a *auditpulumi.Args) { a.Writer.CataloguePaths = []string{writeCatalogue(t, "x.yaml", "a: b\n")} }, "catalogue"},
		"conflict": {func(a *auditpulumi.Args) {
			a.Writer.CataloguePaths = []string{good}
			a.Writer.Catalogues = map[string]string{"catalogue.yaml": "other: 1\n"}
		}, "other content"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := build(t, c.edit); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// ---- the catalogue's data schemas

const (
	schemaCatalogue = "source: app\nversion: \"1.0.0\"\nactions:\n  app.thing.done:\n    data_schema: https://schemas.example/app/v1/thing.json\n"
	thingSchema     = `{"$id": "https://schemas.example/app/v1/thing.json", "type": "object"}`
)

// writeCatalogueDir lays a catalogue out as an application embeds it: the
// document and its schemas in one directory, with something else beside them.
func writeCatalogueDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The writer reads a catalogue with the .json files beside it and refuses to
// start without the schemas it references (seen in production 2026-10-04: every
// record dead-lettered behind a deploy that had succeeded). A catalogue with
// schemas gets a directory of its own, so that no other catalogue is handed
// them; one without stays where it was.
func TestCatalogueDirsShipTheSchemasBesideTheirCatalogue(t *testing.T) {
	dir := writeCatalogueDir(t, map[string]string{
		"catalogue-app.yaml": schemaCatalogue, "thing.json": thingSchema, "README.md": "not shipped",
	})
	rec, _, err := build(t, func(a *auditpulumi.Args) {
		a.Writer.CatalogueDirs = []string{dir}
		a.Writer.Catalogues = map[string]string{"catalogue.yaml": "source: other\nversion: \"1.0.0\"\n"}
	})
	if err != nil {
		t.Fatal(err)
	}
	w := layerFiles(t, rec, "audit-writer")
	want := map[string]string{
		"catalogues/catalogue-app/catalogue-app.yaml": schemaCatalogue,
		"catalogues/catalogue-app/thing.json":         thingSchema,
		"catalogues/catalogue.yaml":                   "source: other\nversion: \"1.0.0\"\n",
	}
	for p, body := range want {
		if w[p] != body {
			t.Errorf("%s = %q, want %q (layer has %v)", p, w[p], body, keys(w))
		}
	}
	for p := range w {
		if strings.HasPrefix(p, "catalogues/") {
			if _, ok := want[p]; !ok {
				t.Errorf("the layer also holds %s", p)
			}
		}
	}
}

func TestCatalogueSchemasGivenAsStringsAreShippedTheSameWay(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) {
		a.Writer.Catalogues = map[string]string{"catalogue-app.yaml": schemaCatalogue}
		a.Writer.CatalogueSchemas = map[string]map[string]string{"catalogue-app.yaml": {"thing.json": thingSchema}}
	})
	if err != nil {
		t.Fatal(err)
	}
	if w := layerFiles(t, rec, "audit-writer"); w["catalogues/catalogue-app/thing.json"] != thingSchema {
		t.Errorf("layer has %v", keys(w))
	}
}

// What the writer would refuse at start-up is refused before anything is
// created.
func TestACatalogueTheWriterWouldRefuseIsRefused(t *testing.T) {
	legacy := strings.Replace(schemaCatalogue, "https://schemas.example/app/v1/thing.json",
		"https://schemas.truvity.com/audit/v1/thing.json", 1)
	for name, c := range map[string]struct {
		edit func(*auditpulumi.Args)
		want string
	}{
		"schema missing": {func(a *auditpulumi.Args) {
			a.Writer.Catalogues = map[string]string{"catalogue-app.yaml": schemaCatalogue}
		}, "references schema https://schemas.example/app/v1/thing.json, which was not supplied"},
		"schema missing from a path": {func(a *auditpulumi.Args) {
			a.Writer.CataloguePaths = []string{writeCatalogue(t, "catalogue-app.yaml", schemaCatalogue)}
		}, "dead-letter queue"},
		"schema unreferenced": {func(a *auditpulumi.Args) {
			a.Writer.Catalogues = map[string]string{"catalogue-app.yaml": "source: app\nversion: \"1.0.0\"\n"}
			a.Writer.CatalogueSchemas = map[string]map[string]string{"catalogue-app.yaml": {"thing.json": thingSchema}}
		}, "supplied but nothing references it"},
		"schemas of no catalogue": {func(a *auditpulumi.Args) {
			a.Writer.CatalogueSchemas = map[string]map[string]string{"catalogue-app.yaml": {"thing.json": thingSchema}}
		}, "not one of the catalogues"},
		"schema without $id": {func(a *auditpulumi.Args) {
			a.Writer.Catalogues = map[string]string{"catalogue-app.yaml": schemaCatalogue}
			a.Writer.CatalogueSchemas = map[string]map[string]string{"catalogue-app.yaml": {"thing.json": `{"type": "object"}`}}
		}, "no $id"},
		"schema not JSON": {func(a *auditpulumi.Args) {
			a.Writer.Catalogues = map[string]string{"catalogue-app.yaml": schemaCatalogue}
			a.Writer.CatalogueSchemas = map[string]map[string]string{"catalogue-app.yaml": {"thing.json": "type: object"}}
		}, "not a JSON object"},
		"schema badly named": {func(a *auditpulumi.Args) {
			a.Writer.Catalogues = map[string]string{"catalogue-app.yaml": schemaCatalogue}
			a.Writer.CatalogueSchemas = map[string]map[string]string{"catalogue-app.yaml": {"../thing.json": thingSchema}}
		}, "<name>.json"},
		"two schemas, one $id": {func(a *auditpulumi.Args) {
			a.Writer.Catalogues = map[string]string{"catalogue-app.yaml": schemaCatalogue}
			a.Writer.CatalogueSchemas = map[string]map[string]string{"catalogue-app.yaml": {"thing.json": thingSchema, "again.json": thingSchema}}
		}, "both claim"},
		"legacy id, schema missing": {func(a *auditpulumi.Args) {
			a.Writer.Catalogues = map[string]string{"catalogue-app.yaml": legacy}
			a.Writer.CatalogueSchemas = map[string]map[string]string{"catalogue-app.yaml": {"thing.json": thingSchema}}
		}, "which was not supplied"},
		"dir with two catalogues": {func(a *auditpulumi.Args) {
			a.Writer.CatalogueDirs = []string{writeCatalogueDir(t, map[string]string{"catalogue-a.yaml": "x: 1\n", "catalogue-b.yaml": "x: 1\n"})}
		}, "holds one catalogue"},
		"dir with none": {func(a *auditpulumi.Args) {
			a.Writer.CatalogueDirs = []string{writeCatalogueDir(t, map[string]string{"thing.json": thingSchema})}
		}, "holds no catalogue"},
		"dir missing": {func(a *auditpulumi.Args) { a.Writer.CatalogueDirs = []string{"/nonexistent"} }, "CatalogueDirs"},
		"dir conflicts": {func(a *auditpulumi.Args) {
			a.Writer.CatalogueDirs = []string{writeCatalogueDir(t, map[string]string{"catalogue-app.yaml": schemaCatalogue, "thing.json": thingSchema})}
			a.Writer.Catalogues = map[string]string{"catalogue-app.yaml": "source: app\nversion: \"2.0.0\"\n"}
		}, "other content"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := build(t, c.edit); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// A legacy id names the same schema as the published one, as the writer reads it.
func TestALegacySchemaIDMatchesThePublishedOne(t *testing.T) {
	doc := strings.Replace(schemaCatalogue, "https://schemas.example/app/v1/thing.json",
		"https://schemas.truvity.com/audit/v1/thing.json", 1)
	if _, _, err := build(t, func(a *auditpulumi.Args) {
		a.Writer.Catalogues = map[string]string{"catalogue-app.yaml": doc}
		a.Writer.CatalogueSchemas = map[string]map[string]string{"catalogue-app.yaml": {
			"thing.json": `{"$id": "https://truvity.github.io/audit/schemas/v1/thing.json"}`,
		}}
	}); err != nil {
		t.Fatal(err)
	}
}
