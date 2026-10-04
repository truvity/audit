package auditpulumi_test

import (
	"strings"
	"testing"

	auditpulumi "github.com/truvity/audit/deploy/pulumi"
)

const givenKey = "arn:aws:kms:eu-west-1:123456789012:key/1234abcd-12ab-34cd-56ef-1234567890ab"

func archiveKeysCreated(rec *recorder) []string {
	var out []string
	for _, ty := range []string{"aws:kms/key:Key", "aws:kms/alias:Alias"} {
		for _, d := range rec.ofType(ty) {
			if d.Name == "audit-archive" {
				out = append(out, ty)
			}
		}
	}
	return out
}

func TestAnExistingKMSKeyIsUsedAndNoneIsCreated(t *testing.T) {
	rec, out, err := build(t, func(a *auditpulumi.Args) {
		a.Archive.KeyArn = givenKey
		a.Observe.IRSA = irsa("audit", "observe")
		a.ArchiveWriter = &auditpulumi.ArchiveWriterArgs{IRSA: *irsa("audit", "digest")}
	})
	if err != nil {
		t.Fatal(err)
	}
	if k := archiveKeysCreated(rec); len(k) != 0 {
		t.Errorf("created %v beside a given key", k)
	}
	// The seal key is the library's own and stays.
	if len(rec.ofType("aws:kms/key:Key")) != 1 {
		t.Errorf("keys: %v", rec.names())
	}
	sse := rec.one(t, "aws:s3/bucketServerSideEncryptionConfiguration:BucketServerSideEncryptionConfiguration", "audit-archive")
	rule := prop(sse, "rules").ArrayValue()[0].ObjectValue()
	def := rule["applyServerSideEncryptionByDefault"].ObjectValue()
	if def["sseAlgorithm"].StringValue() != "aws:kms" || def["kmsMasterKeyId"].StringValue() != givenKey || !rule["bucketKeyEnabled"].BoolValue() {
		t.Errorf("encryption: %v", rule)
	}
	if out["archiveKeyArn"] != givenKey {
		t.Errorf("archiveKeyArn = %q", out["archiveKeyArn"])
	}
	for role, want := range map[string][]string{
		"audit-writer": {"kms:GenerateDataKey", "kms:Decrypt"}, "audit-notary": {"kms:GenerateDataKey", "kms:Decrypt"},
		"audit-observe-reader": {"kms:Decrypt"}, "audit-archive-writer": {"kms:GenerateDataKey", "kms:Decrypt"},
	} {
		g := grants(policy(t, rec, role))
		for _, a := range want {
			if len(g[a]) != 1 || g[a][0] != givenKey {
				t.Errorf("%s %s: %v, want the given key only", role, a, g[a])
			}
		}
		if role == "audit-observe-reader" {
			if _, ok := g["kms:GenerateDataKey"]; ok {
				t.Errorf("the reader may generate data keys")
			}
		}
	}
	for _, fn := range []string{"audit-writer", "audit-notary"} {
		if body := packageFiles(t, rec.one(t, "aws:lambda/function:Function", fn))["audit.yaml"]; !strings.Contains(body, "kmsKey: "+givenKey) {
			t.Errorf("%s does not name the given key:\n%s", fn, body)
		}
	}
}

func TestAWSManagedKeyCreatesNoKeyAndGrantsNoKMS(t *testing.T) {
	rec, out, err := build(t, func(a *auditpulumi.Args) {
		a.Archive.Encryption = auditpulumi.EncryptionAWSManaged
		a.Observe.IRSA = irsa("audit", "observe")
		a.ArchiveWriter = &auditpulumi.ArchiveWriterArgs{IRSA: *irsa("audit", "digest")}
	})
	if err != nil {
		t.Fatal(err)
	}
	sse := rec.one(t, "aws:s3/bucketServerSideEncryptionConfiguration:BucketServerSideEncryptionConfiguration", "audit-archive")
	rule := prop(sse, "rules").ArrayValue()[0].ObjectValue()
	def := rule["applyServerSideEncryptionByDefault"].ObjectValue()
	if def["sseAlgorithm"].StringValue() != "aws:kms" || def["kmsMasterKeyId"].V != nil || !rule["bucketKeyEnabled"].BoolValue() {
		t.Errorf("encryption: %v", rule)
	}
	if k := archiveKeysCreated(rec); len(k) != 0 {
		t.Errorf("created %v with the AWS-managed key", k)
	}
	if out["archiveKeyArn"] != "" {
		t.Errorf("archiveKeyArn = %q", out["archiveKeyArn"])
	}
	for _, role := range []string{"audit-writer", "audit-notary", "audit-observe-reader", "audit-archive-writer"} {
		g := grants(policy(t, rec, role))
		for _, a := range []string{"kms:GenerateDataKey", "kms:Decrypt"} {
			if _, ok := g[a]; ok {
				t.Errorf("%s may %s with the AWS-managed key", role, a)
			}
		}
		if _, ok := g["s3:ListBucket"]; !ok {
			t.Errorf("%s lost its S3 rights", role)
		}
	}
	if g := grants(policy(t, rec, "audit-notary")); len(g["kms:Sign"]) != 1 || g["kms:Sign"][0] != out["sealKeyArn"] {
		t.Errorf("the notary's Sign: %v", g["kms:Sign"])
	}
	for _, fn := range []string{"audit-writer", "audit-notary"} {
		if body := packageFiles(t, rec.one(t, "aws:lambda/function:Function", fn))["audit.yaml"]; strings.Contains(body, "kmsKey") {
			t.Errorf("%s names a key with the AWS-managed key:\n%s", fn, body)
		}
	}
}

func TestTheDefaultKMSModeStillCreatesTheKeyAndAlias(t *testing.T) {
	rec, _, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if k := archiveKeysCreated(rec); len(k) != 2 {
		t.Errorf("created %v, want the key and the alias", k)
	}
	if body := packageFiles(t, rec.one(t, "aws:lambda/function:Function", "audit-writer"))["audit.yaml"]; !strings.Contains(body, "kmsKey: alias/audit-archive") {
		t.Errorf("writer configuration:\n%s", body)
	}
}

func TestSSES3ModeIsUnchangedByTheOtherModes(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) { a.Archive.Encryption = auditpulumi.EncryptionS3 })
	if err != nil {
		t.Fatal(err)
	}
	if got := sseOf(t, rec); got["alg"].S != "AES256" || got["hasKey"].S != "no" {
		t.Errorf("encryption: %v", got)
	}
	if k := archiveKeysCreated(rec); len(k) != 0 {
		t.Errorf("created %v with SSE-S3", k)
	}
}

func TestKeyArnMisuseIsRefused(t *testing.T) {
	for name, c := range map[string]struct {
		edit func(*auditpulumi.Args)
		says string
	}{
		"with s3":          {func(a *auditpulumi.Args) { a.Archive.Encryption, a.Archive.KeyArn = auditpulumi.EncryptionS3, givenKey }, "Archive.KeyArn is only for"},
		"with aws-managed": {func(a *auditpulumi.Args) { a.Archive.Encryption, a.Archive.KeyArn = auditpulumi.EncryptionAWSManaged, givenKey }, "Archive.KeyArn is only for"},
		"an alias ARN": {func(a *auditpulumi.Args) {
			a.Archive.KeyArn = "arn:aws:kms:eu-west-1:123456789012:alias/archive"
		}, "not a KMS key ARN"},
		"a bare key id": {func(a *auditpulumi.Args) { a.Archive.KeyArn = "1234abcd-12ab-34cd-56ef-1234567890ab" }, "not a KMS key ARN"},
	} {
		t.Run(name, func(t *testing.T) {
			rec, _, err := build(t, c.edit)
			if err == nil || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("error = %v, want one saying %q", err, c.says)
			}
			if len(rec.names()) != 0 {
				t.Errorf("created before refusing: %v", rec.names())
			}
		})
	}
}
