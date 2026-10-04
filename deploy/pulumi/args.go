package auditpulumi

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Lock modes of the archive bucket. NONE is a bucket with no Object Lock at all,
// for a period while formats and layout settle; GOVERNANCE is the trial of the
// lock; COMPLIANCE is the target of every record-tier archive and cannot be
// shortened by anyone, including the account's root
// (docs/decisions/0023-archive-retention-and-lifecycle.md). A bucket moves
// NONE -> GOVERNANCE -> COMPLIANCE by editing this field; Object Lock, once
// enabled on a bucket, cannot be disabled.
const (
	None       = "NONE"
	Governance = "GOVERNANCE"
	Compliance = "COMPLIANCE"
)

// Encryption of the archive's objects.
const (
	// EncryptionKMS is SSE-KMS under a key the library creates (alias
	// `<name>-archive`). The default.
	EncryptionKMS = "kms"
	// EncryptionS3 is SSE-S3 (AES256): no archive key is created and no role is
	// granted a kms action on one. The notary's seal key is a different key and
	// is unaffected.
	EncryptionS3 = "s3"
	// EncryptionAWSManaged is SSE-KMS under the AWS-managed key `aws/s3`, with
	// bucket keys: no key is created and no role is granted a kms action. S3
	// itself decrypts for any principal of the account that may s3:GetObject.
	// The key's policy cannot be changed and its use is logged in CloudTrail
	// under the account, not under a key of its own.
	EncryptionAWSManaged = "aws-managed"
)

// Args is everything the library is given. Required fields are named in their
// comments; everything else has the default stated there.
type Args struct {
	// Tags are put on every resource that takes tags.
	Tags map[string]string

	// AccountID is the AWS account the installation lives in. Empty looks it up
	// with sts:GetCallerIdentity, as an invoke made through the component's own
	// provider: the one passed with pulumi.Provider (or pulumi.Providers) to New,
	// or inherited from New's parent. A stack that disables the default providers
	// needs one of those; a stack that cannot reach STS, or that would rather not
	// call it, sets AccountID and no lookup is made at all.
	AccountID string

	// RolePath is the IAM path of every role the library creates. Default
	// "/audit/". The roles are `<name>-writer`, `<name>-notary`,
	// `<name>-observe-reader` and `<name>-scheduler`, where `<name>` is the
	// name the component is registered under, so the default installation
	// ("audit") has the role ARNs
	//
	//	arn:aws:iam::<account>:role/audit/audit-writer
	//	arn:aws:iam::<account>:role/audit/audit-notary
	//	arn:aws:iam::<account>:role/audit/audit-observe-reader
	//
	// which are what a roster or gitops grants name exactly.
	RolePath string

	// LogRetentionDays is how long each function's CloudWatch log group keeps
	// its lines. Default 30. The functions' own telemetry goes over OTLP; this
	// is the platform's copy.
	LogRetentionDays int

	Archive   ArchiveArgs
	Ingest    IngestArgs
	Writer    WriterArgs
	Notary    NotaryArgs
	Telemetry *TelemetryArgs
	Alerts    AlertsArgs
	// Observe, when given, creates the read role audit-observe assumes, from
	// another AWS account or from a Kubernetes workload (IRSA). Nil creates none.
	Observe *ObserveArgs
	// ArchiveWriter, when given, creates a role for a workload outside AWS (a
	// Talos pod) that writes part of the archive itself, by IRSA. Nil creates
	// none.
	ArchiveWriter *ArchiveWriterArgs

	// Guards are the switches of the checks that run before a function is
	// created or updated. See GuardArgs.
	Guards GuardArgs
}

// ArchiveArgs is the archive bucket and its keys.
type ArchiveArgs struct {
	// BucketName is the bucket's name. Required: it is in the functions'
	// configuration, so it has to be known before anything is created, and a
	// bucket name is global.
	BucketName string

	// ObjectLockMode is NONE, GOVERNANCE or COMPLIANCE. Required: there is no
	// default, so that every caller chooses and nobody gets a lock, or no lock,
	// by omission.
	//
	// NONE creates no Object Lock configuration and renders `lockMode: none` for
	// the functions, which then send no retention or legal-hold header and are
	// not granted the permissions for one. The bucket is versioned all the same,
	// so that the lock can be turned on later: moving to GOVERNANCE adds the
	// Object Lock configuration to the existing bucket and replaces nothing.
	// Objects written before that stay unlocked.
	//
	// GOVERNANCE is the trial of the lock: a role holding
	// s3:BypassGovernanceRetention can shorten it. COMPLIANCE cannot be shortened
	// or removed by anyone until each object's retention date, and a retention
	// wrong in the long direction is paid for until then. GOVERNANCE to
	// COMPLIANCE is an edit of the lock configuration of the same bucket, but it
	// is the step nothing undoes: see docs/deployment/aws.md. COMPLIANCE needs
	// AcknowledgeCompliance. Once the lock is on, NONE is refused by AWS, not by
	// this library: Object Lock cannot be disabled on a bucket.
	ObjectLockMode string
	// AcknowledgeCompliance is the deliberate step before COMPLIANCE: true says
	// the retentions were seen working in a governance trial and the deployer has
	// signed them off. With ObjectLockMode COMPLIANCE and this false the library
	// refuses to build anything.
	AcknowledgeCompliance bool
	// DefaultRetentionDays is the bucket's default retention, which applies to an
	// object put with none. The writer sets each object's retention itself, from
	// its profile, so this is a floor and not the policy. Default 0: no default
	// rule. Refused with NONE, where there is no lock for it to be a rule of.
	DefaultRetentionDays int

	// Encryption is "kms" (the default: SSE-KMS under an archive key the library
	// creates) or "s3" (SSE-S3). With "s3" there is no archive key, no
	// `kms:GenerateDataKey` or `kms:Decrypt` grant for it on any role, no
	// `kmsKey` in the functions' configuration, and ArchiveKeyArn is empty. The
	// trade is the key policy and its CloudTrail record of every decrypt. The
	// encryption of existing objects does not change by editing this: S3 keeps
	// what each object was written with.
	//
	// "aws-managed" is SSE-KMS under the AWS-managed key `aws/s3` (bucket keys
	// on): no key is created, no role is granted a kms action, and the
	// functions' configuration names no `kmsKey` (the bucket's default
	// applies). With "kms" and KeyArn set, the key is the caller's, see KeyArn.
	Encryption string
	// KeyArn is an existing symmetric KMS key to encrypt the archive with, in
	// place of the one the library creates. Only with Encryption "kms" (or empty);
	// refused with "s3" and "aws-managed". No key or alias is created, and the
	// writer, notary, observe reader and archive-writer roles are granted
	// `kms:GenerateDataKey` / `kms:Decrypt` (the reader: `kms:Decrypt`) on THIS
	// key through their IAM policies. The key's own policy must therefore allow
	// IAM to grant access (the default policy's `arn:aws:iam::<account>:root`
	// statement does); the library does not edit it and does not protect it.
	// The functions are configured with this ARN as `kmsKey`. Default "": the
	// library creates `alias/<name>-archive`.
	KeyArn string

	// Profiles are the deployment's profile names. Required: a lifecycle rule is
	// written for each `records/<profile>/` prefix, and the profile is the first
	// component of the key for exactly that reason (ADR 0018).
	Profiles []string
	// GlacierIRDays is when a record object moves to Glacier Instant Retrieval:
	// still readable by observe's reindex and by `audit verify` without a
	// restore. Default 30.
	GlacierIRDays int
	// DeepArchiveDays is when it moves to Glacier Deep Archive, which needs a
	// restore to read. Default 365.
	DeepArchiveDays int
}

// IngestArgs is the queue records arrive on.
type IngestArgs struct {
	// Disabled leaves out the whole ingest side: the queue and its dead-letter
	// queue, the deduplication table, the writer function with its role, log
	// group and event source mapping, and the writer's and queue's alarms. For a
	// deployment whose writer runs elsewhere. Writer.Package and
	// Writer.DeploymentYAML are then not required and are ignored.
	Disabled bool
	// Senders are the principals (role or user ARNs) allowed to send to the
	// queue, typically the receivers' roles or the application's. Empty adds no
	// queue policy for senders, so only identity policies in this account grant
	// sending.
	Senders []pulumi.StringInput
	// MaxReceiveCount is how many times a message is delivered before the queue
	// moves it to the dead-letter queue. Default 5.
	MaxReceiveCount int
	// RetentionDays is how long the ingest queue keeps a message, up to 14.
	// Default 14, the most SQS allows, and it bounds the deduplication window
	// from below.
	RetentionDays int
}

// WriterArgs is the writer function.
type WriterArgs struct {
	// Package is the release's zip, `audit-writer-lambda_<version>_linux_arm64.zip`,
	// as a path or an https URL. It is the function's code exactly as released:
	// the library adds nothing to it, and the configuration is a layer
	// (docs/deployment/aws.md#configuration-as-a-layer). Required.
	Package string
	// PackageSHA256 is that zip's SHA-256 in hex, from the release's
	// checksums.txt. Required: the zip is read, hashed and refused when it is not
	// the one named, so nothing is deployed that was not checked.
	PackageSHA256 string
	// DeploymentYAML is the profile configuration (`deployment:` in the
	// function's configuration), which presets each profile is composed from.
	// Required, and the same document the chart renders.
	DeploymentYAML string
	// Catalogues are the application catalogues the writer registers at start-up,
	// by file name (`catalogue.yaml`, `catalogue-<name>.yaml`). The common
	// catalogue is always registered. A function has no registry service.
	Catalogues map[string]string
	// CataloguePaths are catalogue files on disk, read when the stack is
	// evaluated and merged into Catalogues under their base names (which must be
	// `catalogue.yaml` or `catalogue-<name>.yaml`). A name given in both places
	// is refused unless the contents are identical; an unreadable or empty file
	// is refused before anything is created.
	//
	// Either way the catalogue is part of the configuration layer, an immutable
	// layer version, so any change to a catalogue's content makes a new one and
	// points the writer at it on the next `pulumi up`: a catalogue change reaches
	// the writer deliberately, as a deploy, never silently
	// (docs/deployment/aws.md#the-applications-catalogue). A changed catalogue
	// under an unchanged version is refused before the function is updated, see
	// GuardArgs.
	CataloguePaths []string
	// Keys is the `keys:` block of the function's configuration, for a
	// deployment whose profiles pseudonymise. Only a provider reachable from a
	// function outside a VPC is usable: `transit` over a public address. Nil is
	// no keys, which is the default a deployment should have to argue itself out
	// of.
	Keys map[string]any
	// ForgetIdentities is `forgetIdentities` in the configuration.
	ForgetIdentities bool
	// DedupeWindow is `dedupe.dynamodb.window`, a Go duration. Empty is the
	// widest window any profile asks for.
	DedupeWindow string

	// MemoryMB default 512, TimeoutSeconds default 120.
	MemoryMB       int
	TimeoutSeconds int
	// BatchSize is the event source mapping's batch, 1 to 10 for the standard
	// queue (the sink's limit). Default 10.
	BatchSize int
	// MaxBatchingWindowSeconds is how long the mapping gathers a batch before it
	// invokes. Default 5: the objects in the archive are as many as the
	// invocations, and the window is what trades a few seconds of latency for
	// fewer of them.
	MaxBatchingWindowSeconds int
	// MaxConcurrency caps concurrent invocations (the mapping's scaling
	// configuration), 2 or more. Default 10.
	MaxConcurrency int
}

// NotaryArgs is the notary function.
type NotaryArgs struct {
	// Disabled leaves out the whole notary: the P-384 seal key and its alias, the
	// notary function with its role and log group, the schedule and the
	// scheduler's role, and the notary's alarms. For a deployment that seals
	// elsewhere (OpenBao transit) or not yet. Notary.Package is then not
	// required and is ignored.
	Disabled bool
	// Package is the release's zip, `audit-notary-lambda_<version>_linux_arm64.zip`,
	// as a path or an https URL; see WriterArgs.Package. Required.
	Package string
	// PackageSHA256 is that zip's SHA-256 in hex, from the release's
	// checksums.txt. Required.
	PackageSHA256 string
	// Schedule is the EventBridge Scheduler expression, in UTC. Default
	// "cron(15 * * * ? *)": hourly, a quarter past, after the settle window of the
	// hour that has just ended.
	Schedule string
	// Profiles to seal; empty is every profile the archive has records for.
	Profiles []string
	// Settle is `settle` in the configuration, a Go duration. Default "10m".
	Settle string

	// MemoryMB default 256, TimeoutSeconds default 900 (the platform's most).
	MemoryMB       int
	TimeoutSeconds int
}

// TelemetryArgs wires the functions' OpenTelemetry to the OTLP door with the
// function role's own identity and no secret (docs/deployment/aws.md): the
// access-roster Lambda extension is a layer on each function. Nil gives the
// functions no extension, no OTEL_* environment and no sts:GetWebIdentityToken.
type TelemetryArgs struct {
	// ExtensionLayerArn is the layer version of the access-roster OTLP extension,
	// published in this account and region. Required.
	ExtensionLayerArn pulumi.StringInput
	// IssuerURL is the access-roster issuer's base URL. Required.
	IssuerURL string
	// OTLPEndpoint is the OTLP/HTTP base URL, https. Required.
	OTLPEndpoint string
	// STSAudience is the audience asked of STS for the identity token, and the
	// value the roles' policies pin with sts:IdentityTokenAudience. Default
	// "otlp".
	STSAudience string
	// OTLPAudience is the exchange's audience and client id. Default "otlp".
	OTLPAudience string
	// ExtraEnv is other OTEL_* variables, such as OTEL_TRACES_SAMPLER.
	ExtraEnv map[string]string
}

// AlertsArgs is how alarms leave AWS.
type AlertsArgs struct {
	// EndpointURL is the HTTPS endpoint of alert-ingress that the alarm topic
	// delivers to. Empty creates the topic and the alarms and no subscription.
	// alert-ingress must confirm the subscription (SNS sends a
	// SubscriptionConfirmation to the URL), which docs/deployment/aws.md covers.
	EndpointURL pulumi.StringInput
	// OldestMessageAgeSeconds alarms when the oldest message in the ingest queue
	// is older than this. Default 900.
	OldestMessageAgeSeconds int
	// NotarySilenceHours alarms when the notary has not been invoked for this many
	// hours. Default 3, which is two missed hourly runs and a half.
	NotarySilenceHours int
}

// ObserveArgs is the role the observe service reads the archive through.
//
// At least one of TrustedPrincipalArn and IRSA is required; both may be given,
// and the role then trusts either.
type ObserveArgs struct {
	// TrustedPrincipalArn is the principal that may assume the role: in the
	// kernel's account, the role audit-observe runs as.
	TrustedPrincipalArn pulumi.StringInput
	// ExternalID, when set, is required of the assuming principal
	// (sts:ExternalId). It does not apply to IRSA.
	ExternalID string
	// IRSA lets a Kubernetes ServiceAccount assume the role by web identity, for
	// a cluster that is not EKS (Talos) and has an IAM OIDC provider of its own.
	IRSA *IRSAArgs
}

// IRSAArgs names the one ServiceAccount a role trusts through an IAM OIDC
// provider (sts:AssumeRoleWithWebIdentity). The trust policy pins both
// `<issuer>:sub` to `system:serviceaccount:<namespace>:<serviceAccount>` and
// `<issuer>:aud` to the audience: without the sub pin any ServiceAccount of the
// cluster could assume the role.
type IRSAArgs struct {
	// OIDCProviderArn is the IAM OIDC provider of the cluster. Required.
	OIDCProviderArn pulumi.StringInput
	// IssuerHost is the provider's URL without the scheme, the prefix of the
	// condition keys (`k8s.example.com`). Required.
	IssuerHost string
	// Namespace and ServiceAccount of the workload. Required.
	Namespace, ServiceAccount string
	// Audience is the token's audience. Default "sts.amazonaws.com".
	Audience string
}

// ArchiveWriterArgs is an optional role for a workload outside AWS that writes
// some of the archive itself, for example a notary-equivalent digest job on
// Talos that writes `seals/` and `keys/` (its signing key being elsewhere). It
// trusts the one ServiceAccount of IRSA, and may put objects only under Prefixes.
type ArchiveWriterArgs struct {
	IRSA IRSAArgs
	// Prefixes the role may put objects under: any of records/, catalogue/,
	// schema/, identity/, dlq/, seals/ and keys/. Default seals/ and keys/.
	Prefixes []string
}

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// keyComponent is what the store's keys allow of a profile name.
// keyArn is a KMS key ARN, which is what an IAM policy can name; an alias ARN is not.
var keyArn = regexp.MustCompile(`^arn:[a-z-]+:kms:[a-z0-9-]+:[0-9]+:key/[A-Za-z0-9-]+$`)

var keyComponent = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// withDefaults fills what the arguments leave unset, and refuses what cannot
// work. Every refusal names the field.
func (a *Args) withDefaults(name string) (*Args, error) {
	c := *a
	if !nameRE.MatchString(name) {
		return nil, fmt.Errorf("auditpulumi: the component name %q must be 1-32 characters of a-z, 0-9 and -, "+
			"starting with a letter: it is in every role, queue and function name", name)
	}
	if c.RolePath == "" {
		c.RolePath = "/audit/"
	}
	if !strings.HasPrefix(c.RolePath, "/") || !strings.HasSuffix(c.RolePath, "/") {
		return nil, fmt.Errorf("auditpulumi: RolePath %q must start and end with /", c.RolePath)
	}
	setInt(&c.LogRetentionDays, 30)
	if c.AccountID != "" && !accountRE.MatchString(c.AccountID) {
		return nil, fmt.Errorf("auditpulumi: AccountID %q must be the 12 digits of an AWS account id", c.AccountID)
	}

	ar := &c.Archive
	if ar.BucketName == "" {
		return nil, errors.New("auditpulumi: Archive.BucketName is required")
	}
	switch ar.ObjectLockMode {
	case "":
		return nil, errors.New("auditpulumi: Archive.ObjectLockMode is required: NONE, GOVERNANCE or COMPLIANCE (docs/deployment/aws.md)")
	case None, Governance, Compliance:
	default:
		return nil, fmt.Errorf("auditpulumi: Archive.ObjectLockMode %q must be NONE, GOVERNANCE or COMPLIANCE", ar.ObjectLockMode)
	}
	if ar.ObjectLockMode == Compliance && !ar.AcknowledgeCompliance {
		return nil, errors.New("auditpulumi: Archive.ObjectLockMode is COMPLIANCE and Archive.AcknowledgeCompliance is false: " +
			"compliance retention cannot be shortened by anyone, including the account's root, and a retention wrong in the long " +
			"direction is paid for until it expires. Run the governance trial first, sign off the retentions, then set " +
			"AcknowledgeCompliance on a NEW bucket (docs/decisions/0023-archive-retention-and-lifecycle.md)")
	}
	if ar.DefaultRetentionDays < 0 {
		return nil, errors.New("auditpulumi: Archive.DefaultRetentionDays is not negative")
	}
	if ar.ObjectLockMode == None && ar.DefaultRetentionDays > 0 {
		return nil, errors.New("auditpulumi: Archive.DefaultRetentionDays needs a lock: Archive.ObjectLockMode is NONE")
	}
	switch ar.Encryption {
	case "":
		ar.Encryption = EncryptionKMS
	case EncryptionKMS, EncryptionS3, EncryptionAWSManaged:
	default:
		return nil, fmt.Errorf("auditpulumi: Archive.Encryption %q must be %q (the default), %q or %q",
			ar.Encryption, EncryptionKMS, EncryptionAWSManaged, EncryptionS3)
	}
	if ar.KeyArn != "" {
		if ar.Encryption != EncryptionKMS {
			return nil, fmt.Errorf("auditpulumi: Archive.KeyArn is only for Archive.Encryption %q, not %q", EncryptionKMS, ar.Encryption)
		}
		if !keyArn.MatchString(ar.KeyArn) {
			return nil, fmt.Errorf("auditpulumi: Archive.KeyArn %q is not a KMS key ARN (arn:<partition>:kms:<region>:<account>:key/<id>); "+
				"an alias ARN cannot be granted in IAM", ar.KeyArn)
		}
	}
	if len(ar.Profiles) == 0 {
		return nil, errors.New("auditpulumi: Archive.Profiles is required: a lifecycle rule is written for each profile's prefix")
	}
	seen := map[string]bool{}
	for _, p := range ar.Profiles {
		if !keyComponent.MatchString(p) {
			return nil, fmt.Errorf("auditpulumi: Archive.Profiles has %q, which is not a key component (no /, no leading dot)", p)
		}
		if seen[p] {
			return nil, fmt.Errorf("auditpulumi: Archive.Profiles names %q twice", p)
		}
		seen[p] = true
	}
	setInt(&ar.GlacierIRDays, 30)
	setInt(&ar.DeepArchiveDays, 365)
	if ar.DeepArchiveDays <= ar.GlacierIRDays {
		return nil, fmt.Errorf("auditpulumi: Archive.DeepArchiveDays (%d) must be after GlacierIRDays (%d)", ar.DeepArchiveDays, ar.GlacierIRDays)
	}

	in := &c.Ingest
	setInt(&in.MaxReceiveCount, 5)
	setInt(&in.RetentionDays, 14)
	if in.RetentionDays > 14 {
		return nil, errors.New("auditpulumi: Ingest.RetentionDays is at most 14, SQS's own limit")
	}

	w := &c.Writer
	if !in.Disabled {
		if w.Package == "" {
			return nil, errors.New("auditpulumi: Writer.Package is required: the release's audit-writer-lambda_<version>_linux_arm64.zip " +
				"(or set Ingest.Disabled)")
		}
		if !shaRE.MatchString(w.PackageSHA256) {
			return nil, errors.New("auditpulumi: Writer.PackageSHA256 is required: the zip's SHA-256 in hex, from the release's checksums.txt")
		}
		if strings.TrimSpace(w.DeploymentYAML) == "" {
			return nil, errors.New("auditpulumi: Writer.DeploymentYAML is required: the profile configuration (or set Ingest.Disabled)")
		}
	}
	if len(w.CataloguePaths) > 0 {
		merged := make(map[string]string, len(w.Catalogues)+len(w.CataloguePaths))
		for k, v := range w.Catalogues {
			merged[k] = v
		}
		for _, p := range w.CataloguePaths {
			body, err := os.ReadFile(p)
			if err != nil {
				return nil, fmt.Errorf("auditpulumi: Writer.CataloguePaths: %w", err)
			}
			if strings.TrimSpace(string(body)) == "" {
				return nil, fmt.Errorf("auditpulumi: Writer.CataloguePaths: %s is empty", p)
			}
			base := filepath.Base(p)
			if prev, ok := merged[base]; ok && prev != string(body) {
				return nil, fmt.Errorf("auditpulumi: Writer.CataloguePaths: %s is also in Writer.Catalogues with other content", base)
			}
			merged[base] = string(body)
		}
		w.Catalogues = merged
	}
	setInt(&w.MemoryMB, 512)
	setInt(&w.TimeoutSeconds, 120)
	setInt(&w.BatchSize, 10)
	setInt(&w.MaxBatchingWindowSeconds, 5)
	setInt(&w.MaxConcurrency, 10)
	if w.BatchSize > 10 {
		return nil, errors.New("auditpulumi: Writer.BatchSize is at most 10: the sink's batch limit")
	}
	if w.MaxConcurrency < 2 {
		return nil, errors.New("auditpulumi: Writer.MaxConcurrency is at least 2: the event source mapping's own minimum")
	}
	if w.TimeoutSeconds > 900 {
		return nil, errors.New("auditpulumi: Writer.TimeoutSeconds is at most 900")
	}

	n := &c.Notary
	if !n.Disabled {
		if n.Package == "" {
			return nil, errors.New("auditpulumi: Notary.Package is required: the release's audit-notary-lambda_<version>_linux_arm64.zip " +
				"(or set Notary.Disabled)")
		}
		if !shaRE.MatchString(n.PackageSHA256) {
			return nil, errors.New("auditpulumi: Notary.PackageSHA256 is required: the zip's SHA-256 in hex, from the release's checksums.txt")
		}
	}
	if n.Schedule == "" {
		n.Schedule = "cron(15 * * * ? *)"
	}
	if n.Settle == "" {
		n.Settle = "10m"
	}
	setInt(&n.MemoryMB, 256)
	setInt(&n.TimeoutSeconds, 900)
	if n.TimeoutSeconds > 900 {
		return nil, errors.New("auditpulumi: Notary.TimeoutSeconds is at most 900")
	}

	if t := c.Telemetry; t != nil {
		tc := *t
		switch {
		case tc.ExtensionLayerArn == nil:
			return nil, errors.New("auditpulumi: Telemetry.ExtensionLayerArn is required with Telemetry")
		case tc.IssuerURL == "":
			return nil, errors.New("auditpulumi: Telemetry.IssuerURL is required with Telemetry")
		case !strings.HasPrefix(tc.OTLPEndpoint, "https://"):
			return nil, fmt.Errorf("auditpulumi: Telemetry.OTLPEndpoint %q must be an https URL: a bearer token crosses it", tc.OTLPEndpoint)
		}
		if tc.STSAudience == "" {
			tc.STSAudience = "otlp"
		}
		if tc.OTLPAudience == "" {
			tc.OTLPAudience = "otlp"
		}
		for k := range tc.ExtraEnv {
			if !strings.HasPrefix(k, "OTEL_") {
				return nil, fmt.Errorf("auditpulumi: Telemetry.ExtraEnv holds OpenTelemetry SDK variables only: %q does not start with OTEL_", k)
			}
		}
		c.Telemetry = &tc
	}

	setInt(&c.Alerts.OldestMessageAgeSeconds, 900)
	setInt(&c.Alerts.NotarySilenceHours, 3)
	if c.Alerts.NotarySilenceHours > 24 {
		return nil, errors.New("auditpulumi: Alerts.NotarySilenceHours is at most 24")
	}
	if o := c.Observe; o != nil {
		if o.TrustedPrincipalArn == nil && o.IRSA == nil {
			return nil, errors.New("auditpulumi: Observe needs Observe.TrustedPrincipalArn or Observe.IRSA")
		}
		if o.IRSA != nil {
			irsa, err := o.IRSA.withDefaults("Observe.IRSA")
			if err != nil {
				return nil, err
			}
			oc := *o
			oc.IRSA = irsa
			c.Observe = &oc
		}
	}
	if w := c.ArchiveWriter; w != nil {
		irsa, err := w.IRSA.withDefaults("ArchiveWriter.IRSA")
		if err != nil {
			return nil, err
		}
		wc := *w
		wc.IRSA = *irsa
		if len(wc.Prefixes) == 0 {
			wc.Prefixes = append([]string{}, sealPrefixes...)
		}
		allowed := append(append([]string{}, writerPrefixes...), sealPrefixes...)
		seen := map[string]bool{}
		for _, p := range wc.Prefixes {
			if !slices.Contains(allowed, p) {
				return nil, fmt.Errorf("auditpulumi: ArchiveWriter.Prefixes has %q: one of %s", p, strings.Join(allowed, ", "))
			}
			if seen[p] {
				return nil, fmt.Errorf("auditpulumi: ArchiveWriter.Prefixes names %q twice", p)
			}
			seen[p] = true
		}
		c.ArchiveWriter = &wc
	}
	return &c, nil
}

var accountRE = regexp.MustCompile(`^[0-9]{12}$`)

// withDefaults checks one IRSA block; field names the block in the refusal.
func (i IRSAArgs) withDefaults(field string) (*IRSAArgs, error) {
	switch {
	case i.OIDCProviderArn == nil:
		return nil, fmt.Errorf("auditpulumi: %s.OIDCProviderArn is required", field)
	case i.IssuerHost == "":
		return nil, fmt.Errorf("auditpulumi: %s.IssuerHost is required: the OIDC provider's URL without the scheme", field)
	case strings.Contains(i.IssuerHost, "://") || strings.HasSuffix(i.IssuerHost, "/"):
		return nil, fmt.Errorf("auditpulumi: %s.IssuerHost %q is the host (and path), with no scheme and no trailing /: "+
			"it is the prefix of the condition keys", field, i.IssuerHost)
	case i.Namespace == "" || i.ServiceAccount == "":
		return nil, fmt.Errorf("auditpulumi: %s.Namespace and %s.ServiceAccount are required: "+
			"a trust that names no ServiceAccount would be every one in the cluster", field, field)
	case strings.ContainsAny(i.Namespace+i.ServiceAccount, "*?: "):
		return nil, fmt.Errorf("auditpulumi: %s.Namespace and .ServiceAccount are names, not patterns", field)
	}
	if i.Audience == "" {
		i.Audience = "sts.amazonaws.com"
	}
	return &i, nil
}

func setInt(p *int, def int) {
	if *p == 0 {
		*p = def
	}
}
