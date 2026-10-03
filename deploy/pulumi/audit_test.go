package auditpulumi_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	yaml "go.yaml.in/yaml/v3"

	policyconfig "github.com/truvity/policy/config"

	auditpulumi "github.com/truvity/audit/deploy/pulumi"
)

func prop(d declared, key string) resource.PropertyValue { return d.Inputs[resource.PropertyKey(key)] }

// The role names are what gitops grants name exactly, so they are asserted whole.
func TestTheRolesAreNamedExactlyAndLiveUnderTheAuditPath(t *testing.T) {
	_, out, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"writerRole":  "arn:aws:iam::" + account + ":role/audit/audit-writer",
		"notaryRole":  "arn:aws:iam::" + account + ":role/audit/audit-notary",
		"observeRole": "arn:aws:iam::" + account + ":role/audit/audit-observe-reader",
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("%s = %q, want %q", k, out[k], v)
		}
	}
}

func TestAnotherInstallationHasRolesOfItsOwn(t *testing.T) {
	// One account may hold several installations (ADR 0011): the name is in every
	// role, because an IAM role name is unique across the account whatever its path.
	rec, _, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range rec.ofType("aws:iam/role:Role") {
		if !strings.HasPrefix(prop(d, "name").StringValue(), "audit-") {
			t.Errorf("role %s is not prefixed with the installation's name", prop(d, "name").StringValue())
		}
		if prop(d, "path").StringValue() != "/audit/" {
			t.Errorf("role %s is under %s", d.Name, prop(d, "path").StringValue())
		}
	}
}

func TestTheArchiveIsLockedVersionedEncryptedAndClosed(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) { a.Archive.DefaultRetentionDays = 30 })
	if err != nil {
		t.Fatal(err)
	}
	b := rec.one(t, "aws:s3/bucket:Bucket", "audit-archive")
	if !prop(b, "objectLockEnabled").BoolValue() || prop(b, "bucket").StringValue() != "acme-audit" || prop(b, "forceDestroy").BoolValue() {
		t.Errorf("bucket inputs: %v", b.Inputs)
	}
	v := rec.one(t, "aws:s3/bucketVersioning:BucketVersioning", "audit-archive")
	if prop(v, "versioningConfiguration").ObjectValue()["status"].StringValue() != "Enabled" {
		t.Errorf("versioning: %v", v.Inputs)
	}
	lock := rec.one(t, "aws:s3/bucketObjectLockConfiguration:BucketObjectLockConfiguration", "audit-archive")
	def := prop(lock, "rule").ObjectValue()["defaultRetention"].ObjectValue()
	if def["mode"].StringValue() != "GOVERNANCE" || def["days"].NumberValue() != 30 {
		t.Errorf("default retention: %v", def)
	}
	sse := rec.one(t, "aws:s3/bucketServerSideEncryptionConfiguration:BucketServerSideEncryptionConfiguration", "audit-archive")
	rule := prop(sse, "rules").ArrayValue()[0].ObjectValue()
	if rule["applyServerSideEncryptionByDefault"].ObjectValue()["sseAlgorithm"].StringValue() != "aws:kms" {
		t.Errorf("encryption: %v", rule)
	}
	pab := rec.one(t, "aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock", "audit-archive")
	for _, k := range []string{"blockPublicAcls", "blockPublicPolicy", "ignorePublicAcls", "restrictPublicBuckets"} {
		if !prop(pab, k).BoolValue() {
			t.Errorf("public access block: %s is not set", k)
		}
	}
	own := rec.one(t, "aws:s3/bucketOwnershipControls:BucketOwnershipControls", "audit-archive")
	if prop(own, "rule").ObjectValue()["objectOwnership"].StringValue() != "BucketOwnerEnforced" {
		t.Errorf("ownership: %v", own.Inputs)
	}
}

func TestNoDefaultRetentionRuleUnlessAskedFor(t *testing.T) {
	rec, _, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	lock := rec.one(t, "aws:s3/bucketObjectLockConfiguration:BucketObjectLockConfiguration", "audit-archive")
	if prop(lock, "rule").IsObject() {
		t.Errorf("a default retention was set that nobody asked for: %v", lock.Inputs)
	}
}

func TestLifecycleIsPerProfilePrefixAtThirtyDaysAndOneYear(t *testing.T) {
	rec, _, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	lc := rec.one(t, "aws:s3/bucketLifecycleConfiguration:BucketLifecycleConfiguration", "audit-archive")
	got := map[string][2]float64{}
	for _, r := range prop(lc, "rules").ArrayValue() {
		o := r.ObjectValue()
		trs := o["transitions"]
		if !trs.IsArray() {
			continue
		}
		var days [2]float64
		for _, tr := range trs.ArrayValue() {
			switch tr.ObjectValue()["storageClass"].StringValue() {
			case "GLACIER_IR":
				days[0] = tr.ObjectValue()["days"].NumberValue()
			case "DEEP_ARCHIVE":
				days[1] = tr.ObjectValue()["days"].NumberValue()
			}
		}
		got[o["filter"].ObjectValue()["prefix"].StringValue()] = days
	}
	want := map[string][2]float64{"records/security/": {30, 365}, "records/billing-nl/": {30, 365}}
	if len(got) != len(want) {
		t.Fatalf("rules by prefix = %v, want %v", got, want)
	}
	for p, d := range want {
		if got[p] != d {
			t.Errorf("%s: %v, want %v", p, got[p], d)
		}
	}
	// And a rule never reaches seals/, keys/ or catalogue/: they are small and read often.
	for p := range got {
		if !strings.HasPrefix(p, "records/") {
			t.Errorf("a transition rule on %q", p)
		}
	}
}

func TestLifecycleDaysAreParameters(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) { a.Archive.GlacierIRDays, a.Archive.DeepArchiveDays = 90, 730 })
	if err != nil {
		t.Fatal(err)
	}
	lc := rec.one(t, "aws:s3/bucketLifecycleConfiguration:BucketLifecycleConfiguration", "audit-archive")
	for _, r := range prop(lc, "rules").ArrayValue() {
		if trs := r.ObjectValue()["transitions"]; trs.IsArray() {
			if d := trs.ArrayValue()[0].ObjectValue()["days"].NumberValue(); d != 90 {
				t.Errorf("glacier days = %v", d)
			}
		}
	}
}

func TestComplianceNeedsAnAcknowledgementAndThenIsProtected(t *testing.T) {
	if _, _, err := build(t, func(a *auditpulumi.Args) { a.Archive.ObjectLockMode = auditpulumi.Compliance }); err == nil ||
		!strings.Contains(err.Error(), "AcknowledgeCompliance") {
		t.Fatalf("COMPLIANCE without the acknowledgement: %v", err)
	}
	rec, _, err := build(t, func(a *auditpulumi.Args) {
		a.Archive.ObjectLockMode, a.Archive.AcknowledgeCompliance, a.Archive.DefaultRetentionDays = auditpulumi.Compliance, true, 365
	})
	if err != nil {
		t.Fatal(err)
	}
	lock := rec.one(t, "aws:s3/bucketObjectLockConfiguration:BucketObjectLockConfiguration", "audit-archive")
	if m := prop(lock, "rule").ObjectValue()["defaultRetention"].ObjectValue()["mode"].StringValue(); m != "COMPLIANCE" {
		t.Errorf("mode = %s", m)
	}
}

func TestKeysAreAnArchiveKeyAndAP384SigningKeyOnlyTheNotaryCanUse(t *testing.T) {
	rec, _, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	ak := rec.one(t, "aws:kms/key:Key", "audit-archive")
	if !prop(ak, "enableKeyRotation").BoolValue() || prop(ak, "keyUsage").IsString() {
		t.Errorf("archive key: %v", ak.Inputs)
	}
	sk := rec.one(t, "aws:kms/key:Key", "audit-seal")
	if prop(sk, "customerMasterKeySpec").StringValue() != "ECC_NIST_P384" || prop(sk, "keyUsage").StringValue() != "SIGN_VERIFY" {
		t.Errorf("seal key: %v", sk.Inputs)
	}
	// The key policy names the notary and keeps the account root out of Sign.
	pol := prop(sk, "policy").StringValue()
	if !strings.Contains(pol, "arn:aws:iam::"+account+":role/audit/audit-notary") {
		t.Errorf("the seal key's policy does not name the notary's role: %s", pol)
	}
	for _, s := range policyOf(t, pol) {
		principal := s["Principal"].(map[string]any)["AWS"].(string)
		for _, a := range strs(s["Action"]) {
			if strings.HasSuffix(principal, ":root") && (a == "kms:Sign" || a == "kms:*" || a == "kms:Decrypt") {
				t.Errorf("the account root may %s with the seal key", a)
			}
		}
	}
	aliases := map[string]bool{}
	for _, a := range rec.ofType("aws:kms/alias:Alias") {
		aliases[prop(a, "name").StringValue()] = true
	}
	if !aliases["alias/audit-archive"] || !aliases["alias/audit-seal"] {
		t.Errorf("aliases = %v", aliases)
	}
}

func TestTheIngestQueueRedrivesToADeadLetterQueue(t *testing.T) {
	rec, _, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	q := rec.one(t, "aws:sqs/queue:Queue", "audit-ingest")
	if rp := prop(q, "redrivePolicy").StringValue(); !strings.Contains(rp, "audit-ingest-dlq") || !strings.Contains(rp, `"maxReceiveCount":5`) {
		t.Errorf("redrive policy: %s", rp)
	}
	// Visibility is six times the writer's 120s timeout, which Lambda asks of a source queue.
	if v := prop(q, "visibilityTimeoutSeconds").NumberValue(); v != 720 {
		t.Errorf("visibility timeout = %v", v)
	}
	if !prop(q, "sqsManagedSseEnabled").BoolValue() || prop(q, "messageRetentionSeconds").NumberValue() != 14*24*3600 {
		t.Errorf("queue: %v", q.Inputs)
	}
	dlq := rec.one(t, "aws:sqs/queue:Queue", "audit-ingest-dlq")
	if prop(dlq, "redrivePolicy").IsString() {
		t.Error("the DLQ redrives onwards")
	}
}

func TestTheQueuePolicyNamesTheSendersAndDeniesPlainHTTP(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) {
		a.Ingest.Senders = []pulumi.StringInput{pulumi.String("arn:aws:iam::999988887777:role/app/receiver")}
	})
	if err != nil {
		t.Fatal(err)
	}
	p := rec.one(t, "aws:sqs/queuePolicy:QueuePolicy", "audit-ingest")
	pol := prop(p, "policy").StringValue()
	if !strings.Contains(pol, "role/app/receiver") || !strings.Contains(pol, "aws:SecureTransport") {
		t.Errorf("queue policy: %s", pol)
	}
}

func TestTheDedupeTableIsOnDemandWithTTLOnExpiresAt(t *testing.T) {
	rec, _, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := rec.one(t, "aws:dynamodb/table:Table", "audit-dedupe")
	if prop(d, "name").StringValue() != "audit-dedupe" || prop(d, "hashKey").StringValue() != "pk" ||
		prop(d, "billingMode").StringValue() != "PAY_PER_REQUEST" ||
		prop(d, "ttl").ObjectValue()["attributeName"].StringValue() != "expires_at" || !prop(d, "ttl").ObjectValue()["enabled"].BoolValue() {
		t.Errorf("table: %v", d.Inputs)
	}
}

func TestTheFunctionsRunOutsideAVPCOnArm64WithTheExtensionLayer(t *testing.T) {
	rec, _, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"audit-writer", "audit-notary"} {
		f := rec.one(t, "aws:lambda/function:Function", name)
		if prop(f, "runtime").StringValue() != "provided.al2023" || prop(f, "handler").StringValue() != "bootstrap" {
			t.Errorf("%s: %v", name, f.Inputs)
		}
		if a := prop(f, "architectures").ArrayValue(); len(a) != 1 || a[0].StringValue() != "arm64" {
			t.Errorf("%s architectures: %v", name, a)
		}
		if prop(f, "vpcConfig").IsObject() {
			t.Errorf("%s is in a VPC", name)
		}
		if l := prop(f, "layers").ArrayValue(); len(l) != 1 || !strings.Contains(l[0].StringValue(), "access-roster-otlp") {
			t.Errorf("%s layers: %v", name, l)
		}
		env := prop(f, "environment").ObjectValue()["variables"].ObjectValue()
		for k, want := range map[string]string{
			"OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:4318",
			"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
			"OTEL_SERVICE_NAME":           "audit-" + strings.TrimPrefix(name, "audit-"),
			"ACCESS_ROSTER_AUDIENCE":      "otlp",
			"ACCESS_ROSTER_OTLP_AUDIENCE": "otlp",
			"ACCESS_ROSTER_ISSUER":        "https://access.example.test",
			"ACCESS_ROSTER_OTLP_ENDPOINT": "https://otlp.example.test",
		} {
			if env[resource.PropertyKey(k)].StringValue() != want {
				t.Errorf("%s: %s = %q, want %q", name, k, env[resource.PropertyKey(k)].StringValue(), want)
			}
		}
		for k := range env {
			if strings.Contains(strings.ToLower(string(k)), "secret") || strings.Contains(strings.ToLower(string(k)), "token") {
				t.Errorf("%s carries %s: a function holds no secret", name, k)
			}
		}
	}
}

func TestWithoutTelemetryThereIsNoExtensionNoEnvironmentAndNoWebIdentity(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) { a.Telemetry = nil })
	if err != nil {
		t.Fatal(err)
	}
	f := rec.one(t, "aws:lambda/function:Function", "audit-writer")
	if prop(f, "environment").IsObject() || len(prop(f, "layers").ArrayValue()) != 0 {
		t.Errorf("telemetry left behind: %v", f.Inputs)
	}
	for _, role := range []string{"audit-writer", "audit-notary"} {
		if _, ok := grants(policy(t, rec, role))["sts:GetWebIdentityToken"]; ok {
			t.Errorf("%s may ask STS for a token with telemetry off", role)
		}
	}
}

func TestTheEventSourceMappingReportsBatchItemFailures(t *testing.T) {
	rec, _, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := rec.one(t, "aws:lambda/eventSourceMapping:EventSourceMapping", "audit-writer")
	if r := prop(m, "functionResponseTypes").ArrayValue(); len(r) != 1 || r[0].StringValue() != "ReportBatchItemFailures" {
		t.Errorf("response types: %v", r)
	}
	if prop(m, "batchSize").NumberValue() != 10 || prop(m, "scalingConfig").ObjectValue()["maximumConcurrency"].NumberValue() != 10 {
		t.Errorf("mapping: %v", m.Inputs)
	}
}

func TestTheWriterMayWriteTheArchiveAndNothingElse(t *testing.T) {
	rec, _, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	g := grants(policy(t, rec, "audit-writer"))
	for _, a := range []string{"s3:PutObject", "s3:PutObjectRetention", "s3:PutObjectLegalHold"} {
		if !hasResource(g, a, "/records/*") {
			t.Errorf("the writer may not %s records", a)
		}
		for _, bad := range []string{"/seals/*", "/keys/*"} {
			if hasResource(g, a, bad) {
				t.Errorf("the writer may %s %s", a, bad)
			}
		}
	}
	for _, a := range []string{"dynamodb:BatchGetItem", "dynamodb:PutItem", "sqs:ReceiveMessage", "sqs:DeleteMessage", "kms:GenerateDataKey"} {
		if len(g[a]) == 0 {
			t.Errorf("the writer may not %s", a)
		}
	}
	for a := range g {
		switch {
		case strings.HasPrefix(a, "kms:") && a != "kms:GenerateDataKey" && a != "kms:Decrypt":
			t.Errorf("the writer has %s: it must not sign", a)
		case strings.Contains(a, "Delete") && !strings.HasPrefix(a, "sqs:"):
			t.Errorf("the writer has %s: nothing in the archive is deleted", a)
		}
	}
	if hasResource(g, "kms:GenerateDataKey", "key/audit-seal") {
		t.Error("the writer's KMS rights reach the seal key")
	}
}

func TestTheNotaryReadsPutsSealsAndSignsWithTheSealKeyOnly(t *testing.T) {
	rec, out, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	g := grants(policy(t, rec, "audit-notary"))
	for _, a := range []string{"s3:PutObject", "s3:PutObjectRetention"} {
		if !hasResource(g, a, "/seals/*") || !hasResource(g, a, "/keys/*") {
			t.Errorf("the notary may not %s seals and keys", a)
		}
		if hasResource(g, a, "/records/*") {
			t.Errorf("the notary may %s records", a)
		}
	}
	if !hasResource(g, "s3:GetObject", "/records/*") || len(g["s3:ListBucket"]) == 0 {
		t.Error("the notary cannot read what it seals")
	}
	for _, a := range []string{"kms:Sign", "kms:GetPublicKey"} {
		if len(g[a]) != 1 || g[a][0] != out["sealKeyArn"] {
			t.Errorf("%s is granted on %v, want the seal key %s only", a, g[a], out["sealKeyArn"])
		}
	}
	if _, ok := g["dynamodb:PutItem"]; ok {
		t.Error("the notary has the dedupe table")
	}
}

func TestBothRolesPinTheWebIdentityAudienceWithForAllValues(t *testing.T) {
	rec, _, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"audit-writer", "audit-notary"} {
		var found bool
		for _, s := range policy(t, rec, role) {
			if strs(s["Action"])[0] != "sts:GetWebIdentityToken" {
				continue
			}
			found = true
			c := s["Condition"].(map[string]any)
			fav, ok := c["ForAllValues:StringEquals"].(map[string]any)
			if !ok {
				t.Fatalf("%s: no ForAllValues:StringEquals in %v", role, c)
			}
			if aud := strs(fav["sts:IdentityTokenAudience"]); len(aud) != 1 || aud[0] != "otlp" {
				t.Errorf("%s: audience %v, want [otlp]", role, aud)
			}
			if _, bad := c["StringEquals"].(map[string]any)["sts:IdentityTokenAudience"]; bad {
				t.Errorf("%s: a plain StringEquals on the multi-valued audience key is an implicit deny", role)
			}
		}
		if !found {
			t.Errorf("%s may not ask STS for a token", role)
		}
	}
}

func TestTheAudienceIsAParameter(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) { a.Telemetry.STSAudience = "https://access.example.test" })
	if err != nil {
		t.Fatal(err)
	}
	f := rec.one(t, "aws:lambda/function:Function", "audit-notary")
	if v := prop(f, "environment").ObjectValue()["variables"].ObjectValue()["ACCESS_ROSTER_AUDIENCE"].StringValue(); v != "https://access.example.test" {
		t.Errorf("ACCESS_ROSTER_AUDIENCE = %q", v)
	}
	if !strings.Contains(prop(rec.one(t, "aws:iam/rolePolicy:RolePolicy", "audit-writer"), "policy").StringValue(), `"https://access.example.test"`) {
		t.Error("the role policy does not pin the audience the function asks for")
	}
}

func TestTheNotaryIsScheduledHourlyWithoutRetriesAndItsRoleCanOnlyInvokeIt(t *testing.T) {
	rec, out, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := rec.one(t, "aws:scheduler/schedule:Schedule", "audit-notary")
	if prop(s, "scheduleExpression").StringValue() != "cron(15 * * * ? *)" || prop(s, "scheduleExpressionTimezone").StringValue() != "UTC" {
		t.Errorf("schedule: %v", s.Inputs)
	}
	tg := prop(s, "target").ObjectValue()
	if tg["arn"].StringValue() != out["notaryFn"] || tg["retryPolicy"].ObjectValue()["maximumRetryAttempts"].NumberValue() != 0 {
		t.Errorf("target: %v", tg)
	}
	if !strings.HasSuffix(tg["roleArn"].StringValue(), "role/audit/audit-scheduler") {
		t.Errorf("the scheduler runs as %s", tg["roleArn"].StringValue())
	}
	g := grants(policy(t, rec, "audit-scheduler"))
	if len(g) != 1 || len(g["lambda:InvokeFunction"]) == 0 {
		t.Errorf("the scheduler's rights: %v", g)
	}
	if c := rec.one(t, "aws:lambda/functionEventInvokeConfig:FunctionEventInvokeConfig", "audit-notary"); prop(c, "maximumRetryAttempts").NumberValue() != 0 {
		t.Errorf("async retries: %v", c.Inputs)
	}
}

func TestTheD13AlarmSetExistsAndPublishesToTheTopic(t *testing.T) {
	rec, out, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]declared{}
	for _, a := range rec.ofType("aws:cloudwatch/metricAlarm:MetricAlarm") {
		got[prop(a, "name").StringValue()] = a
	}
	var want []string
	for _, n := range auditpulumi.AlarmNames {
		want = append(want, "audit-"+n)
	}
	var have []string
	for n := range got {
		have = append(have, n)
	}
	sort.Strings(want)
	sort.Strings(have)
	if strings.Join(have, ",") != strings.Join(want, ",") {
		t.Fatalf("alarms = %v, want %v", have, want)
	}
	check := func(alarm, ns, metric, op string, threshold float64, missing string) {
		a := got["audit-"+alarm]
		if prop(a, "namespace").StringValue() != ns || prop(a, "metricName").StringValue() != metric ||
			prop(a, "comparisonOperator").StringValue() != op || prop(a, "threshold").NumberValue() != threshold ||
			prop(a, "treatMissingData").StringValue() != missing {
			t.Errorf("%s: %v", alarm, a.Inputs)
		}
		for _, k := range []string{"alarmActions", "okActions"} {
			acts := prop(a, k).ArrayValue()
			if len(acts) != 1 || acts[0].StringValue() != out["topic"] {
				t.Errorf("%s %s = %v, want the alarm topic", alarm, k, acts)
			}
		}
	}
	check("writer-throttles", "AWS/Lambda", "Throttles", "GreaterThanThreshold", 0, "notBreaching")
	check("notary-throttles", "AWS/Lambda", "Throttles", "GreaterThanThreshold", 0, "notBreaching")
	check("writer-errors", "AWS/Lambda", "Errors", "GreaterThanThreshold", 0, "notBreaching")
	check("notary-errors", "AWS/Lambda", "Errors", "GreaterThanThreshold", 0, "notBreaching")
	check("ingest-dlq-not-empty", "AWS/SQS", "ApproximateNumberOfMessagesVisible", "GreaterThanThreshold", 0, "notBreaching")
	check("ingest-oldest-message-age", "AWS/SQS", "ApproximateAgeOfOldestMessage", "GreaterThanThreshold", 900, "notBreaching")
	check("notary-silent", "AWS/Lambda", "Invocations", "LessThanThreshold", 1, "breaching")

	// The DLQ alarm watches the DLQ, the age alarm the queue, and the silence alarm
	// the notary over the whole window.
	if q := prop(got["audit-ingest-dlq-not-empty"], "dimensions").ObjectValue()["QueueName"].StringValue(); q != "audit-ingest-dlq" {
		t.Errorf("the DLQ alarm watches %s", q)
	}
	if q := prop(got["audit-ingest-oldest-message-age"], "dimensions").ObjectValue()["QueueName"].StringValue(); q != "audit-ingest" {
		t.Errorf("the age alarm watches %s", q)
	}
	silent := got["audit-notary-silent"]
	if prop(silent, "evaluationPeriods").NumberValue() != 3 || prop(silent, "datapointsToAlarm").NumberValue() != 3 ||
		prop(silent, "period").NumberValue() != 3600 {
		t.Errorf("the silence window: %v", silent.Inputs)
	}
	if prop(silent, "dimensions").ObjectValue()["FunctionName"].StringValue() != "audit-notary" {
		t.Errorf("the silence alarm watches %v", prop(silent, "dimensions"))
	}
}

func TestAlarmsReachAlertIngressOverAnHTTPSSubscription(t *testing.T) {
	rec, _, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	sub := rec.one(t, "aws:sns/topicSubscription:TopicSubscription", "audit-alert-ingress")
	if prop(sub, "protocol").StringValue() != "https" || prop(sub, "endpoint").StringValue() != "https://alerts.example.test/sns" ||
		prop(sub, "endpointAutoConfirms").BoolValue() {
		t.Errorf("subscription: %v", sub.Inputs)
	}
	rec, _, err = build(t, func(a *auditpulumi.Args) { a.Alerts.EndpointURL = nil })
	if err != nil {
		t.Fatal(err)
	}
	if n := len(rec.ofType("aws:sns/topicSubscription:TopicSubscription")); n != 0 {
		t.Errorf("%d subscriptions without an endpoint", n)
	}
	if n := len(rec.ofType("aws:cloudwatch/metricAlarm:MetricAlarm")); n != len(auditpulumi.AlarmNames) {
		t.Errorf("%d alarms without an endpoint", n)
	}
}

func TestTheObserveRoleTrustsTheGivenPrincipalAndReadsFourPrefixes(t *testing.T) {
	rec, out, err := build(t, func(a *auditpulumi.Args) { a.Observe.ExternalID = "kernel-observe" })
	if err != nil {
		t.Fatal(err)
	}
	r := rec.one(t, "aws:iam/role:Role", "audit-observe-reader")
	trust := prop(r, "assumeRolePolicy").StringValue()
	if !strings.Contains(trust, "arn:aws:iam::999988887777:role/kernel/audit-observe") || !strings.Contains(trust, "sts:ExternalId") {
		t.Errorf("trust: %s", trust)
	}
	g := grants(policy(t, rec, "audit-observe-reader"))
	for _, p := range []string{"/records/*", "/catalogue/*", "/seals/*", "/keys/*"} {
		if !hasResource(g, "s3:GetObject", p) {
			t.Errorf("observe cannot read %s", p)
		}
	}
	for a := range g {
		if strings.HasPrefix(a, "s3:Put") || strings.HasPrefix(a, "s3:Delete") || a == "kms:Sign" || a == "kms:GenerateDataKey" {
			t.Errorf("observe may %s", a)
		}
	}
	if g["kms:Decrypt"][0] != out["archiveKeyArn"] {
		t.Errorf("observe decrypts under %v", g["kms:Decrypt"])
	}
	if hasResource(g, "s3:GetObject", "/identity/*") || hasResource(g, "s3:GetObject", "/dlq/*") {
		t.Error("observe reads what it should not")
	}
}

func TestNoObserveRoleWithoutObserve(t *testing.T) {
	rec, out, err := build(t, func(a *auditpulumi.Args) { a.Observe = nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rec.ofType("aws:iam/role:Role") {
		if strings.Contains(r.Name, "observe") {
			t.Errorf("role %s without Observe", r.Name)
		}
	}
	if out["observeRole"] != "" {
		t.Errorf("observeRole = %q", out["observeRole"])
	}
}

func TestNothingIsCreatedForArgumentsThatCannotWork(t *testing.T) {
	for name, c := range map[string]struct {
		edit func(*auditpulumi.Args)
		says string
	}{
		"no bucket":       {func(a *auditpulumi.Args) { a.Archive.BucketName = "" }, "BucketName"},
		"no profiles":     {func(a *auditpulumi.Args) { a.Archive.Profiles = nil }, "Profiles"},
		"a bad profile":   {func(a *auditpulumi.Args) { a.Archive.Profiles = []string{"a/b"} }, "key component"},
		"a mode":          {func(a *auditpulumi.Args) { a.Archive.ObjectLockMode = "NONE" }, "GOVERNANCE or COMPLIANCE"},
		"days":            {func(a *auditpulumi.Args) { a.Archive.GlacierIRDays, a.Archive.DeepArchiveDays = 400, 30 }, "after"},
		"no deployment":   {func(a *auditpulumi.Args) { a.Writer.DeploymentYAML = " " }, "DeploymentYAML"},
		"no binary":       {func(a *auditpulumi.Args) { a.Notary.BinaryPath = "" }, "BinaryPath"},
		"retention":       {func(a *auditpulumi.Args) { a.Ingest.RetentionDays = 30 }, "14"},
		"batch":           {func(a *auditpulumi.Args) { a.Writer.BatchSize = 50 }, "BatchSize"},
		"an http OTLP":    {func(a *auditpulumi.Args) { a.Telemetry.OTLPEndpoint = "http://otlp" }, "https"},
		"a bad env":       {func(a *auditpulumi.Args) { a.Telemetry.ExtraEnv = map[string]string{"AWS_X": "y"} }, "OTEL_"},
		"a bad catalogue": {func(a *auditpulumi.Args) { a.Writer.Catalogues = map[string]string{"../x.yaml": "a: b"} }, "catalogue"},
		"a role path":     {func(a *auditpulumi.Args) { a.RolePath = "audit" }, "RolePath"},
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

// The configuration the library ships in each function's package is held to the
// schema of the binary that reads it, so the library cannot drift from the code
// it deploys: a key renamed in schemas/config fails here.
func TestTheShippedConfigurationsValidateAgainstTheBinariesSchemas(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) {
		a.Writer.Catalogues = map[string]string{"catalogue.yaml": "source: app\n"}
		a.Writer.ForgetIdentities = true
		a.Writer.DedupeWindow = "336h"
		a.Notary.Profiles = []string{"security"}
	})
	if err != nil {
		t.Fatal(err)
	}
	for fn, schema := range map[string]string{"audit-writer": "audit-writer-lambda", "audit-notary": "audit-notary"} {
		f := rec.one(t, "aws:lambda/function:Function", fn)
		files := packageFiles(t, f)
		body, ok := files["audit.yaml"]
		if !ok {
			t.Fatalf("%s: no audit.yaml in the package; have %v", fn, keys(files))
		}
		var doc any
		if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join("..", "..", "schemas", "config", schema+".schema.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := policyconfig.Validate(doc, raw); err != nil {
			t.Errorf("%s's configuration does not validate against %s: %v\n%s", fn, schema, err, body)
		}
	}
}

func TestThePackageHoldsTheBinaryTheConfigurationAndTheProfilesAndCatalogues(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) {
		a.Writer.Catalogues = map[string]string{"catalogue.yaml": "source: app\n"}
	})
	if err != nil {
		t.Fatal(err)
	}
	w := packageFiles(t, rec.one(t, "aws:lambda/function:Function", "audit-writer"))
	for _, f := range []string{"bootstrap", "audit.yaml", "deployment.yaml", "catalogues/catalogue.yaml"} {
		if _, ok := w[f]; !ok {
			t.Errorf("the writer's package lacks %s; has %v", f, keys(w))
		}
	}
	for _, want := range []string{"deployment: /var/task/deployment.yaml", "catalogues: /var/task/catalogues", "lockMode: governance",
		"kmsKey: alias/audit-archive", "table: audit-dedupe", "name: acme-audit", "require: archived"} {
		if !strings.Contains(w["audit.yaml"], want) {
			t.Errorf("the writer's configuration lacks %q:\n%s", want, w["audit.yaml"])
		}
	}
	n := packageFiles(t, rec.one(t, "aws:lambda/function:Function", "audit-notary"))
	for _, want := range []string{"key: alias/audit-seal", "lockMode: governance", "settle: 10m"} {
		if !strings.Contains(n["audit.yaml"], want) {
			t.Errorf("the notary's configuration lacks %q:\n%s", want, n["audit.yaml"])
		}
	}
	// The configuration holds no secret and no ARN of anything created later.
	for _, body := range []string{w["audit.yaml"], n["audit.yaml"]} {
		if strings.Contains(body, "arn:") || strings.Contains(strings.ToLower(body), "secret") {
			t.Errorf("a configuration with an ARN or a secret:\n%s", body)
		}
	}
}

func TestTheComplianceModeReachesTheFunctionsConfiguration(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) {
		a.Archive.ObjectLockMode, a.Archive.AcknowledgeCompliance = auditpulumi.Compliance, true
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"audit-writer", "audit-notary"} {
		if body := packageFiles(t, rec.one(t, "aws:lambda/function:Function", fn))["audit.yaml"]; !strings.Contains(body, "lockMode: compliance") {
			t.Errorf("%s writes with %s", fn, body)
		}
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
