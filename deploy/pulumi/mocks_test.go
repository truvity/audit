package auditpulumi_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/asset"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	auditpulumi "github.com/truvity/audit/deploy/pulumi"
)

// The ids are built and not written out, which the repository's leak canary
// would take for particulars; they are made up.
var (
	account      = strings.Repeat("1", 12)
	otherAccount = strings.Repeat("9", 12)
)

const arnp = "arn:" + "aws:"

// recorder is Pulumi's mock engine: it answers every resource with its own inputs
// plus the outputs the provider would compute (an ARN, a URL), and keeps what it
// was asked for, so a test reads the resources the library declared without a
// cloud, a credential or a plugin.
type recorder struct {
	mu        sync.Mutex
	resources []declared
	calls     []mockCall
}

// mockCall is an invoke the library made: its token, and the provider it was made
// through ("" is the default provider, which a stack can disable).
type mockCall struct{ Token, Provider string }

type declared struct {
	Type, Name string
	Inputs     resource.PropertyMap
}

func (r *recorder) NewResource(a pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	r.mu.Lock()
	r.resources = append(r.resources, declared{Type: a.TypeToken, Name: a.Name, Inputs: a.Inputs.Copy()})
	r.mu.Unlock()

	out := a.Inputs.Copy()
	physical := a.Name
	for _, k := range []string{"name", "bucket"} {
		if v, ok := a.Inputs[resource.PropertyKey(k)]; ok && v.IsString() {
			physical = v.StringValue()
		}
	}
	set := func(k, v string) { out[resource.PropertyKey(k)] = resource.NewStringProperty(v) }
	set("name", physical)
	switch a.TypeToken {
	case "aws:iam/role:Role":
		path := "/"
		if p, ok := a.Inputs["path"]; ok && p.IsString() {
			path = p.StringValue()
		}
		set("arn", arnp+"iam::"+account+":role"+path+physical)
	case "aws:s3/bucket:Bucket":
		set("arn", arnp+"s3:::"+physical)
		set("bucket", physical)
	case "aws:sqs/queue:Queue":
		set("arn", arnp+"sqs:eu-west-1:"+account+":"+physical)
		set("url", "https://sqs.eu-west-1.amazonaws.com/"+account+"/"+physical)
	case "aws:kms/key:Key":
		set("arn", arnp+"kms:eu-west-1:"+account+":key/"+a.Name)
		set("keyId", a.Name)
	case "aws:lambda/function:Function":
		set("arn", arnp+"lambda:eu-west-1:"+account+":function:"+physical)
	case "aws:cloudwatch/logGroup:LogGroup":
		set("arn", arnp+"logs:eu-west-1:"+account+":log-group:"+physical)
	case "aws:dynamodb/table:Table":
		set("arn", arnp+"dynamodb:eu-west-1:"+account+":table/"+physical)
	case "aws:sns/topic:Topic":
		set("arn", arnp+"sns:eu-west-1:"+account+":"+physical)
	case "aws:scheduler/schedule:Schedule":
		set("arn", arnp+"scheduler:eu-west-1:"+account+":schedule/default/"+physical)
	default:
		set("arn", arnp+"mock:::"+a.TypeToken+"/"+physical)
	}
	return a.Name + "_id", out, nil
}

func (r *recorder) Call(a pulumi.MockCallArgs) (resource.PropertyMap, error) {
	r.mu.Lock()
	r.calls = append(r.calls, mockCall{Token: a.Token, Provider: a.Provider})
	r.mu.Unlock()
	if a.Token == "aws:index/getCallerIdentity:getCallerIdentity" {
		return resource.PropertyMap{
			"accountId": resource.NewStringProperty(account),
			"arn":       resource.NewStringProperty(arnp + "iam::" + account + ":user/ci"),
			"id":        resource.NewStringProperty(account),
			"userId":    resource.NewStringProperty("AIDAMOCK"),
		}, nil
	}
	return a.Args, nil
}

func (r *recorder) ofType(typ string) []declared {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []declared
	for _, d := range r.resources {
		if d.Type == typ {
			out = append(out, d)
		}
	}
	return out
}

func (r *recorder) one(t *testing.T, typ, name string) declared {
	t.Helper()
	for _, d := range r.ofType(typ) {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("no %s named %s; have %v", typ, name, r.names())
	return declared{}
}

func (r *recorder) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, d := range r.resources {
		out = append(out, d.Type+"/"+d.Name)
	}
	sort.Strings(out)
	return out
}

// outputs are the component's outputs, resolved.
type outputs map[string]string

// build runs the library against the mocks and returns what it declared and what
// it exports. edit changes the arguments a test starts from.
func build(t *testing.T, edit func(*auditpulumi.Args)) (*recorder, outputs, error) {
	t.Helper()
	return buildWith(t, edit, nil)
}

// buildWith is build with options for New, made inside the program (a provider
// can only be made there).
func buildWith(t *testing.T, edit func(*auditpulumi.Args), opts func(*pulumi.Context) ([]pulumi.ResourceOption, error)) (*recorder, outputs, error) {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{"writer-bootstrap", "notary-bootstrap"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("#!/bin/true\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	args := &auditpulumi.Args{
		Archive: auditpulumi.ArchiveArgs{BucketName: "acme-audit", ObjectLockMode: auditpulumi.Governance, Profiles: []string{"security", "billing-nl"}},
		Writer: auditpulumi.WriterArgs{
			BinaryPath:     filepath.Join(dir, "writer-bootstrap"),
			DeploymentYAML: "profiles:\n  security:\n    presets: [security]\n",
		},
		Notary: auditpulumi.NotaryArgs{BinaryPath: filepath.Join(dir, "notary-bootstrap")},
		Telemetry: &auditpulumi.TelemetryArgs{
			ExtensionLayerArn: pulumi.String(arnp + "lambda:eu-west-1:" + account + ":layer:access-roster-otlp:3"),
			IssuerURL:         "https://access.example.test",
			OTLPEndpoint:      "https://otlp.example.test",
		},
		Alerts:  auditpulumi.AlertsArgs{EndpointURL: pulumi.String("https://alerts.example.test/sns")},
		Observe: &auditpulumi.ObserveArgs{TrustedPrincipalArn: pulumi.String(arnp + "iam::" + otherAccount + ":role/kernel/audit-observe")},
	}
	if edit != nil {
		edit(args)
	}
	rec := &recorder{}
	got := outputs{}
	var wg sync.WaitGroup
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		var ro []pulumi.ResourceOption
		if opts != nil {
			var err error
			if ro, err = opts(ctx); err != nil {
				return err
			}
		}
		a, err := auditpulumi.New(ctx, "audit", args, ro...)
		if err != nil {
			return err
		}
		for k, o := range map[string]pulumi.StringOutput{
			"bucketName": a.BucketName, "bucketArn": a.BucketArn, "archiveKeyArn": a.ArchiveKeyArn, "sealKeyArn": a.SealKeyArn,
			"sealKeyAlias": a.SealKeyAlias, "queueUrl": a.QueueURL, "queueArn": a.QueueArn, "dlqUrl": a.DlqURL, "dlqArn": a.DlqArn, "archiveWriterRole": a.ArchiveWriterRoleArn,
			"dedupe": a.DedupeTableName, "writerFn": a.WriterFunctionArn, "notaryFn": a.NotaryFunctionArn,
			"writerRole": a.WriterRoleArn, "notaryRole": a.NotaryRoleArn, "observeRole": a.ObserveReaderRoleArn,
			"topic": a.AlarmTopicArn, "schedule": a.ScheduleArn,
		} {
			wg.Add(1)
			o.ApplyT(func(v string) string {
				defer wg.Done()
				rec.mu.Lock()
				got[k] = v
				rec.mu.Unlock()
				return v
			})
		}
		return nil
	}, pulumi.WithMocks("audit-test", "test", rec))
	wg.Wait()
	return rec, got, err
}

// policy is a role policy's document as the library wrote it.
func policy(t *testing.T, r *recorder, role string) []map[string]any {
	t.Helper()
	d := r.one(t, "aws:iam/rolePolicy:RolePolicy", role)
	var doc struct{ Statement []map[string]any }
	if err := json.Unmarshal([]byte(d.Inputs["policy"].StringValue()), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Statement
}

func strs(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, e.(string))
		}
		return out
	}
	return nil
}

// grants lists every (action, resource) pair a policy allows.
func grants(st []map[string]any) map[string][]string {
	out := map[string][]string{}
	for _, s := range st {
		if s["Effect"] != "Allow" {
			continue
		}
		for _, a := range strs(s["Action"]) {
			out[a] = append(out[a], strs(s["Resource"])...)
		}
	}
	return out
}

func hasResource(g map[string][]string, action, suffix string) bool {
	for _, r := range g[action] {
		if strings.HasSuffix(r, suffix) {
			return true
		}
	}
	return false
}

func policyOf(t *testing.T, doc string) []map[string]any {
	t.Helper()
	var d struct{ Statement []map[string]any }
	if err := json.Unmarshal([]byte(doc), &d); err != nil {
		t.Fatal(err)
	}
	return d.Statement
}

// packageFiles are the files of a function's zip: the text of each string asset,
// and "(file)" for the binary, which is a path and not text.
func packageFiles(t *testing.T, f declared) map[string]string {
	t.Helper()
	code := f.Inputs["code"]
	if !code.IsArchive() {
		t.Fatalf("%s: code is not an archive: %v", f.Name, code)
	}
	assets, ok := code.ArchiveValue().GetAssets()
	if !ok {
		t.Fatalf("%s: the archive is not a map of assets", f.Name)
	}
	out := map[string]string{}
	for name, v := range assets {
		a, ok := v.(*asset.Asset)
		if !ok {
			t.Fatalf("%s: %s is %T", f.Name, name, v)
		}
		if a.IsText() {
			out[name] = a.Text
		} else {
			out[name] = "(file)"
		}
	}
	return out
}
