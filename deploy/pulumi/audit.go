// Package auditpulumi is the AWS shape of the audit trail as a Pulumi Go
// library: the archive bucket and its keys, the ingest queue, the writer and
// notary Lambda functions with the role of each, the schedule that invokes the
// notary, and the alarms that say when any of it is not working.
//
// It is a library, not a program: a stack calls New with the arguments below
// and gets a component with the outputs a deployment needs. It creates nothing
// by being imported, this repository deploys nothing with it, and it is a
// module of its own (github.com/truvity/audit/deploy/pulumi) so that Pulumi is
// not in the root module's dependency graph.
//
//	a, err := auditpulumi.New(ctx, "audit", &auditpulumi.Args{
//		Archive: auditpulumi.ArchiveArgs{BucketName: "acme-audit", Profiles: []string{"security"}},
//		Writer:  auditpulumi.WriterArgs{BinaryPath: "writer/bootstrap", DeploymentYAML: deployment},
//		Notary:  auditpulumi.NotaryArgs{BinaryPath: "notary/bootstrap"},
//	})
//
// docs/deployment/aws.md is the guide: the shape, every input and output, the
// switch from GOVERNANCE to COMPLIANCE, the role names and how alarms reach
// alert-ingress.
package auditpulumi

import (
	"fmt"
	"strings"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudwatch"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/dynamodb"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/kms"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/scheduler"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/sqs"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// ComponentType is the Pulumi type token of the component.
const ComponentType = "truvity:audit:Audit"

// Audit is the component. Its fields are the outputs.
type Audit struct {
	pulumi.ResourceState

	// BucketName and BucketArn are the archive bucket.
	BucketName pulumi.StringOutput
	BucketArn  pulumi.StringOutput
	// ArchiveKeyArn is the symmetric key objects are encrypted with; SealKeyArn
	// is the P-384 key seals are signed with, and SealKeyAlias its alias, which
	// is what the notary's configuration names.
	ArchiveKeyArn pulumi.StringOutput
	SealKeyArn    pulumi.StringOutput
	SealKeyAlias  pulumi.StringOutput
	// QueueURL and QueueArn are the ingest queue a receiver or an application
	// sends to; DlqURL and DlqArn its dead-letter queue.
	QueueURL pulumi.StringOutput
	QueueArn pulumi.StringOutput
	DlqURL   pulumi.StringOutput
	DlqArn   pulumi.StringOutput
	// DedupeTableName is the DynamoDB table of written record ids.
	DedupeTableName pulumi.StringOutput
	// WriterFunctionArn and NotaryFunctionArn are the functions.
	WriterFunctionArn pulumi.StringOutput
	NotaryFunctionArn pulumi.StringOutput
	// WriterRoleArn and NotaryRoleArn are the roles the functions run as: the
	// ARNs a roster or gitops grant names, and the identities the OTLP door
	// sees. ObserveReaderRoleArn is the cross-account read role, empty without
	// Args.Observe.
	WriterRoleArn        pulumi.StringOutput
	NotaryRoleArn        pulumi.StringOutput
	ObserveReaderRoleArn pulumi.StringOutput
	// AlarmTopicArn is the SNS topic every alarm publishes to.
	AlarmTopicArn pulumi.StringOutput
	// ScheduleArn is the notary's schedule.
	ScheduleArn pulumi.StringOutput
}

// New creates the component and everything under it. name is the installation's
// name, and is in every physical name the library gives: `<name>-writer`,
// `<name>-ingest`, and so on, so one account may hold several installations.
func New(ctx *pulumi.Context, name string, args *Args, opts ...pulumi.ResourceOption) (*Audit, error) {
	a, err := args.withDefaults(name)
	if err != nil {
		return nil, err
	}
	out := &Audit{}
	if err := ctx.RegisterComponentResource(ComponentType, name, out, opts...); err != nil {
		return nil, err
	}
	child := pulumi.Parent(out)
	tags := pulumi.ToStringMap(a.Tags)

	identity, err := aws.GetCallerIdentity(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("auditpulumi: the caller's account: %w", err)
	}
	accountRoot := fmt.Sprintf("arn:aws:iam::%s:root", identity.AccountId)

	// What each function's package holds beside its binary is rendered first, so
	// an argument that cannot be rendered fails before anything is created.
	writerPackage, err := writerFiles(name, a)
	if err != nil {
		return nil, err
	}
	notaryPackage, err := notaryFiles(name, a)
	if err != nil {
		return nil, err
	}

	// ---- the roles come first: the seal key's policy names the notary's.
	writerRole, err := newRole(ctx, name+"-writer", a.RolePath, assumeRoleJSON("lambda.amazonaws.com"), tags, child)
	if err != nil {
		return nil, err
	}
	notaryRole, err := newRole(ctx, name+"-notary", a.RolePath, assumeRoleJSON("lambda.amazonaws.com"), tags, child)
	if err != nil {
		return nil, err
	}

	// ---- keys
	archiveKey, err := kms.NewKey(ctx, name+"-archive", &kms.KeyArgs{
		Description:          pulumi.Sprintf("%s: the key the archive's objects are encrypted with", name),
		EnableKeyRotation:    pulumi.Bool(true),
		DeletionWindowInDays: pulumi.Int(30),
		Tags:                 tags,
	}, child)
	if err != nil {
		return nil, err
	}
	if _, err := kms.NewAlias(ctx, name+"-archive", &kms.AliasArgs{
		Name: pulumi.String(archiveKeyAlias(name)), TargetKeyId: archiveKey.KeyId,
	}, child); err != nil {
		return nil, err
	}
	sealKey, err := kms.NewKey(ctx, name+"-seal", &kms.KeyArgs{
		Description:           pulumi.Sprintf("%s: the P-384 key seals are signed with (ES384)", name),
		CustomerMasterKeySpec: pulumi.String("ECC_NIST_P384"),
		KeyUsage:              pulumi.String("SIGN_VERIFY"),
		DeletionWindowInDays:  pulumi.Int(30),
		Policy:                notaryRole.Arn.ApplyT(func(arn string) string { return sealKeyPolicy(accountRoot, arn) }).(pulumi.StringOutput),
		Tags:                  tags,
	}, child)
	if err != nil {
		return nil, err
	}
	if _, err := kms.NewAlias(ctx, name+"-seal", &kms.AliasArgs{
		Name: pulumi.String(sealKeyAlias(name)), TargetKeyId: sealKey.KeyId,
	}, child); err != nil {
		return nil, err
	}

	// ---- the archive
	bucket, err := newArchive(ctx, name, a, archiveKey, tags, child)
	if err != nil {
		return nil, err
	}

	// ---- the queue, its dead-letter queue and the deduplication table
	queue, dlq, err := newQueues(ctx, name, a, tags, child)
	if err != nil {
		return nil, err
	}
	table, err := dynamodb.NewTable(ctx, name+"-dedupe", &dynamodb.TableArgs{
		Name:        pulumi.String(dedupeTable(name)),
		BillingMode: pulumi.String("PAY_PER_REQUEST"),
		HashKey:     pulumi.String("pk"),
		Attributes:  dynamodb.TableAttributeArray{&dynamodb.TableAttributeArgs{Name: pulumi.String("pk"), Type: pulumi.String("S")}},
		// The attribute the writer sets on each item; DynamoDB deletes an item
		// after it, lazily, which is why the store also checks the time itself.
		Ttl:  &dynamodb.TableTtlArgs{AttributeName: pulumi.String("expires_at"), Enabled: pulumi.Bool(true)},
		Tags: tags,
	}, child)
	if err != nil {
		return nil, err
	}

	// ---- the functions
	writerFn, writerLogs, err := newFunction(ctx, functionSpec{
		Name: name + "-writer", Service: writerService, Role: writerRole, Binary: a.Writer.BinaryPath,
		MemoryMB: a.Writer.MemoryMB, TimeoutSeconds: a.Writer.TimeoutSeconds,
		Files: writerPackage,
	}, a, tags, child)
	if err != nil {
		return nil, err
	}
	notaryFn, notaryLogs, err := newFunction(ctx, functionSpec{
		Name: name + "-notary", Service: notaryService, Role: notaryRole, Binary: a.Notary.BinaryPath,
		MemoryMB: a.Notary.MemoryMB, TimeoutSeconds: a.Notary.TimeoutSeconds,
		Files: notaryPackage,
	}, a, tags, child)
	if err != nil {
		return nil, err
	}

	// ---- each role's policy
	audience := ""
	if a.Telemetry != nil {
		audience = a.Telemetry.STSAudience
	}
	if _, err := iam.NewRolePolicy(ctx, name+"-writer", &iam.RolePolicyArgs{
		Role: writerRole.Name,
		Policy: pulumi.All(bucket.Arn, archiveKey.Arn, table.Arn, queue.Arn, writerLogs.Arn).ApplyT(func(v []any) string {
			return writerPolicy(v[0].(string), v[1].(string), v[2].(string), v[3].(string), v[4].(string), audience)
		}).(pulumi.StringOutput),
	}, child); err != nil {
		return nil, err
	}
	if _, err := iam.NewRolePolicy(ctx, name+"-notary", &iam.RolePolicyArgs{
		Role: notaryRole.Name,
		Policy: pulumi.All(bucket.Arn, archiveKey.Arn, sealKey.Arn, notaryLogs.Arn).ApplyT(func(v []any) string {
			return notaryPolicy(v[0].(string), v[1].(string), v[2].(string), v[3].(string), audience)
		}).(pulumi.StringOutput),
	}, child); err != nil {
		return nil, err
	}

	// ---- the queue feeds the writer
	if _, err := lambda.NewEventSourceMapping(ctx, name+"-writer", &lambda.EventSourceMappingArgs{
		EventSourceArn:                 queue.Arn,
		FunctionName:                   writerFn.Arn,
		BatchSize:                      pulumi.Int(a.Writer.BatchSize),
		MaximumBatchingWindowInSeconds: pulumi.Int(a.Writer.MaxBatchingWindowSeconds),
		// Partial batch responses: a message that was archived is not redelivered
		// because another in its batch failed.
		FunctionResponseTypes: pulumi.StringArray{pulumi.String("ReportBatchItemFailures")},
		ScalingConfig:         &lambda.EventSourceMappingScalingConfigArgs{MaximumConcurrency: pulumi.Int(a.Writer.MaxConcurrency)},
	}, child); err != nil {
		return nil, err
	}

	// ---- the notary's schedule
	schedule, err := newSchedule(ctx, name, a, notaryFn, tags, child)
	if err != nil {
		return nil, err
	}

	// ---- alarms
	topic, err := newAlarms(ctx, name, a, alarmTargets{
		Queue: queue, Dlq: dlq, Writer: writerFn, Notary: notaryFn,
	}, tags, child)
	if err != nil {
		return nil, err
	}

	// ---- the cross-account read role
	observeArn := pulumi.String("").ToStringOutput()
	if a.Observe != nil {
		role, err := newObserveReader(ctx, name, a, bucket, archiveKey, tags, child)
		if err != nil {
			return nil, err
		}
		observeArn = role.Arn
	}

	out.BucketName, out.BucketArn = bucket.Bucket, bucket.Arn
	out.ArchiveKeyArn, out.SealKeyArn = archiveKey.Arn, sealKey.Arn
	out.SealKeyAlias = pulumi.String(sealKeyAlias(name)).ToStringOutput()
	out.QueueURL, out.QueueArn, out.DlqURL, out.DlqArn = queue.Url, queue.Arn, dlq.Url, dlq.Arn
	out.DedupeTableName = table.Name
	out.WriterFunctionArn, out.NotaryFunctionArn = writerFn.Arn, notaryFn.Arn
	out.WriterRoleArn, out.NotaryRoleArn, out.ObserveReaderRoleArn = writerRole.Arn, notaryRole.Arn, observeArn
	out.AlarmTopicArn = topic.Arn
	out.ScheduleArn = schedule.Arn
	if err := ctx.RegisterResourceOutputs(out, pulumi.Map{
		"bucketName": out.BucketName, "bucketArn": out.BucketArn,
		"archiveKeyArn": out.ArchiveKeyArn, "sealKeyArn": out.SealKeyArn, "sealKeyAlias": out.SealKeyAlias,
		"queueUrl": out.QueueURL, "queueArn": out.QueueArn, "dlqUrl": out.DlqURL, "dlqArn": out.DlqArn,
		"dedupeTableName":   out.DedupeTableName,
		"writerFunctionArn": out.WriterFunctionArn, "notaryFunctionArn": out.NotaryFunctionArn,
		"writerRoleArn": out.WriterRoleArn, "notaryRoleArn": out.NotaryRoleArn, "observeReaderRoleArn": out.ObserveReaderRoleArn,
		"alarmTopicArn": out.AlarmTopicArn, "scheduleArn": out.ScheduleArn,
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func newRole(ctx *pulumi.Context, name, path, assume string, tags pulumi.StringMap, opts ...pulumi.ResourceOption) (*iam.Role, error) {
	return iam.NewRole(ctx, name, &iam.RoleArgs{
		Name: pulumi.String(name), Path: pulumi.String(path), AssumeRolePolicy: pulumi.String(assume), Tags: tags,
	}, opts...)
}

// newArchive is the bucket: Object Lock in the given mode, versioned, encrypted
// under the archive key, closed to the public and to plain HTTP, with the
// lifecycle of ADR 0023 written per profile prefix.
func newArchive(ctx *pulumi.Context, name string, a *Args, key *kms.Key, tags pulumi.StringMap, opts ...pulumi.ResourceOption) (*s3.Bucket, error) {
	ar := a.Archive
	// A COMPLIANCE bucket is the one resource here that cannot be undone, so it
	// is protected from a stack's own destroy: Pulumi refuses to delete it until
	// the protection is lifted by hand, which is a decision and not an accident.
	// (S3 would refuse to delete the objects anyway; this refuses sooner.)
	bopts := append([]pulumi.ResourceOption{}, opts...)
	if ar.ObjectLockMode == Compliance {
		bopts = append(bopts, pulumi.Protect(true))
	}
	bucket, err := s3.NewBucket(ctx, name+"-archive", &s3.BucketArgs{
		Bucket:            pulumi.String(ar.BucketName),
		ObjectLockEnabled: pulumi.Bool(true),
		// A bucket with objects under lock cannot be emptied; never offer to.
		ForceDestroy: pulumi.Bool(false),
		Tags:         tags,
	}, bopts...)
	if err != nil {
		return nil, err
	}
	versioning, err := s3.NewBucketVersioning(ctx, name+"-archive", &s3.BucketVersioningArgs{
		Bucket:                  bucket.ID(),
		VersioningConfiguration: &s3.BucketVersioningVersioningConfigurationArgs{Status: pulumi.String("Enabled")},
	}, opts...)
	if err != nil {
		return nil, err
	}
	lock := &s3.BucketObjectLockConfigurationArgs{
		Bucket: bucket.ID(), ObjectLockEnabled: pulumi.String("Enabled"),
	}
	if ar.DefaultRetentionDays > 0 {
		lock.Rule = &s3.BucketObjectLockConfigurationRuleArgs{
			DefaultRetention: &s3.BucketObjectLockConfigurationRuleDefaultRetentionArgs{
				Mode: pulumi.String(ar.ObjectLockMode), Days: pulumi.Int(ar.DefaultRetentionDays),
			},
		}
	}
	if _, err := s3.NewBucketObjectLockConfiguration(ctx, name+"-archive", lock,
		append([]pulumi.ResourceOption{pulumi.DependsOn([]pulumi.Resource{versioning})}, opts...)...); err != nil {
		return nil, err
	}
	if _, err := s3.NewBucketServerSideEncryptionConfiguration(ctx, name+"-archive", &s3.BucketServerSideEncryptionConfigurationArgs{
		Bucket: bucket.ID(),
		Rules: s3.BucketServerSideEncryptionConfigurationRuleArray{&s3.BucketServerSideEncryptionConfigurationRuleArgs{
			ApplyServerSideEncryptionByDefault: &s3.BucketServerSideEncryptionConfigurationRuleApplyServerSideEncryptionByDefaultArgs{
				SseAlgorithm: pulumi.String("aws:kms"), KmsMasterKeyId: key.Arn,
			},
			// One data key per bucket and period instead of one KMS call per
			// object: the writer puts an object per batch.
			BucketKeyEnabled: pulumi.Bool(true),
		}},
	}, opts...); err != nil {
		return nil, err
	}
	if _, err := s3.NewBucketPublicAccessBlock(ctx, name+"-archive", &s3.BucketPublicAccessBlockArgs{
		Bucket:                bucket.ID(),
		BlockPublicAcls:       pulumi.Bool(true),
		BlockPublicPolicy:     pulumi.Bool(true),
		IgnorePublicAcls:      pulumi.Bool(true),
		RestrictPublicBuckets: pulumi.Bool(true),
	}, opts...); err != nil {
		return nil, err
	}
	if _, err := s3.NewBucketOwnershipControls(ctx, name+"-archive", &s3.BucketOwnershipControlsArgs{
		Bucket: bucket.ID(),
		Rule:   &s3.BucketOwnershipControlsRuleArgs{ObjectOwnership: pulumi.String("BucketOwnerEnforced")},
	}, opts...); err != nil {
		return nil, err
	}
	if _, err := s3.NewBucketPolicy(ctx, name+"-archive", &s3.BucketPolicyArgs{
		Bucket: bucket.ID(),
		Policy: bucket.Arn.ApplyT(func(arn string) string {
			return policyJSON(statement{
				"Sid": "OnlyOverTLS", "Effect": "Deny", "Principal": "*", "Action": "s3:*",
				"Resource":  []string{arn, arn + "/*"},
				"Condition": map[string]any{"Bool": map[string]any{"aws:SecureTransport": "false"}},
			})
		}).(pulumi.StringOutput),
	}, opts...); err != nil {
		return nil, err
	}

	// ADR 0023: Glacier Instant Retrieval after GlacierIRDays, which observe's
	// reindex and `audit verify` read without a restore; Deep Archive after
	// DeepArchiveDays, for what nobody expects to read before its retention ends.
	// A rule per profile, because the profile is the first component of the key
	// so that lifecycle can differ by it. Objects below the storage class's
	// minimum billable size stay where they are (the S3 default).
	rules := s3.BucketLifecycleConfigurationRuleArray{}
	for _, p := range ar.Profiles {
		rules = append(rules, &s3.BucketLifecycleConfigurationRuleArgs{
			Id:     pulumi.String("records-" + p),
			Status: pulumi.String("Enabled"),
			Filter: &s3.BucketLifecycleConfigurationRuleFilterArgs{Prefix: pulumi.String("records/" + p + "/")},
			Transitions: s3.BucketLifecycleConfigurationRuleTransitionArray{
				&s3.BucketLifecycleConfigurationRuleTransitionArgs{Days: pulumi.Int(ar.GlacierIRDays), StorageClass: pulumi.String("GLACIER_IR")},
				&s3.BucketLifecycleConfigurationRuleTransitionArgs{Days: pulumi.Int(ar.DeepArchiveDays), StorageClass: pulumi.String("DEEP_ARCHIVE")},
			},
		})
	}
	rules = append(rules, &s3.BucketLifecycleConfigurationRuleArgs{
		Id:     pulumi.String("abort-incomplete-multipart-uploads"),
		Status: pulumi.String("Enabled"),
		Filter: &s3.BucketLifecycleConfigurationRuleFilterArgs{Prefix: pulumi.String("")},
		AbortIncompleteMultipartUpload: &s3.BucketLifecycleConfigurationRuleAbortIncompleteMultipartUploadArgs{
			DaysAfterInitiation: pulumi.Int(7),
		},
	})
	if _, err := s3.NewBucketLifecycleConfiguration(ctx, name+"-archive", &s3.BucketLifecycleConfigurationArgs{
		Bucket: bucket.ID(), Rules: rules,
	}, append([]pulumi.ResourceOption{pulumi.DependsOn([]pulumi.Resource{versioning})}, opts...)...); err != nil {
		return nil, err
	}
	return bucket, nil
}

// newQueues is the ingest queue and the dead-letter queue its messages move to
// after MaxReceiveCount deliveries. Both are encrypted with SQS-managed keys: a
// queue holds records for as long as the writer is behind, and a customer key
// would put the senders' key permissions into the design for no gain here.
func newQueues(ctx *pulumi.Context, name string, a *Args, tags pulumi.StringMap, opts ...pulumi.ResourceOption) (*sqs.Queue, *sqs.Queue, error) {
	retention := a.Ingest.RetentionDays * 24 * 3600
	dlq, err := sqs.NewQueue(ctx, name+"-ingest-dlq", &sqs.QueueArgs{
		Name:                    pulumi.String(name + "-ingest-dlq"),
		MessageRetentionSeconds: pulumi.Int(14 * 24 * 3600),
		SqsManagedSseEnabled:    pulumi.Bool(true),
		Tags:                    tags,
	}, opts...)
	if err != nil {
		return nil, nil, err
	}
	queue, err := sqs.NewQueue(ctx, name+"-ingest", &sqs.QueueArgs{
		Name:                    pulumi.String(name + "-ingest"),
		MessageRetentionSeconds: pulumi.Int(retention),
		// Six times the function's timeout, which is what Lambda asks of an event
		// source queue: a message must not become visible again while its
		// invocation is still running.
		VisibilityTimeoutSeconds: pulumi.Int(6 * a.Writer.TimeoutSeconds),
		SqsManagedSseEnabled:     pulumi.Bool(true),
		RedrivePolicy: dlq.Arn.ApplyT(func(arn string) (string, error) {
			return fmt.Sprintf(`{"deadLetterTargetArn":%q,"maxReceiveCount":%d}`, arn, a.Ingest.MaxReceiveCount), nil
		}).(pulumi.StringOutput),
		Tags: tags,
	}, opts...)
	if err != nil {
		return nil, nil, err
	}
	if _, err := sqs.NewRedriveAllowPolicy(ctx, name+"-ingest-dlq", &sqs.RedriveAllowPolicyArgs{
		QueueUrl: dlq.Url,
		RedriveAllowPolicy: queue.Arn.ApplyT(func(arn string) string {
			return fmt.Sprintf(`{"redrivePermission":"byQueue","sourceQueueArns":[%q]}`, arn)
		}).(pulumi.StringOutput),
	}, opts...); err != nil {
		return nil, nil, err
	}
	// Who may send. Always TLS only; and, when senders are named, them.
	senders := pulumi.StringArray{}
	for _, s := range a.Ingest.Senders {
		senders = append(senders, s)
	}
	if _, err := sqs.NewQueuePolicy(ctx, name+"-ingest", &sqs.QueuePolicyArgs{
		QueueUrl: queue.Url,
		Policy: pulumi.All(queue.Arn, senders).ApplyT(func(v []any) string {
			arn, who := v[0].(string), v[1].([]string)
			st := []statement{{
				"Sid": "OnlyOverTLS", "Effect": "Deny", "Principal": "*", "Action": "sqs:*", "Resource": arn,
				"Condition": map[string]any{"Bool": map[string]any{"aws:SecureTransport": "false"}},
			}}
			if len(who) > 0 {
				st = append(st, statement{
					"Sid": "Senders", "Effect": "Allow", "Principal": map[string]any{"AWS": who},
					"Action": []string{"sqs:SendMessage"}, "Resource": arn,
				})
			}
			return policyJSON(st...)
		}).(pulumi.StringOutput),
	}, opts...); err != nil {
		return nil, nil, err
	}
	return queue, dlq, nil
}

// functionSpec is what differs between the two functions.
type functionSpec struct {
	Name, Service  string
	Role           *iam.Role
	Binary         string
	MemoryMB       int
	TimeoutSeconds int
	// Files are what the package holds beside the binary, by path.
	Files map[string]string
}

// newFunction is one Lambda function: a zip of `bootstrap` and its configuration
// on provided.al2023, on arm64, outside any VPC, with the OTLP extension as a
// layer when there is one.
//
// A zip and not an image: the binaries are static and a few MB, a function's
// configuration is one more file in the package, and nothing here needs a
// registry. The package is the release's `bootstrap` plus the files rendered
// from the stack's own arguments, which is why the configuration needs no fetch
// at cold start (docs/deployment/aws.md).
func newFunction(ctx *pulumi.Context, s functionSpec, a *Args, tags pulumi.StringMap,
	opts ...pulumi.ResourceOption) (*lambda.Function, *cloudwatch.LogGroup, error) {
	logs, err := cloudwatch.NewLogGroup(ctx, s.Name, &cloudwatch.LogGroupArgs{
		Name:            pulumi.String("/aws/lambda/" + s.Name),
		RetentionInDays: pulumi.Int(a.LogRetentionDays),
		Tags:            tags,
	}, opts...)
	if err != nil {
		return nil, nil, err
	}
	archive := map[string]any{"bootstrap": pulumi.NewFileAsset(s.Binary)}
	for path, body := range s.Files {
		archive[path] = pulumi.NewStringAsset(body)
	}
	env := pulumi.StringMap{}
	for k, v := range telemetryEnv(a.Telemetry, s.Service) {
		env[k] = pulumi.String(v)
	}
	layers := pulumi.StringArray{}
	if a.Telemetry != nil {
		layers = append(layers, a.Telemetry.ExtensionLayerArn)
	}
	args := &lambda.FunctionArgs{
		Name:          pulumi.String(s.Name),
		Role:          s.Role.Arn,
		Runtime:       pulumi.String("provided.al2023"),
		Handler:       pulumi.String("bootstrap"),
		Architectures: pulumi.StringArray{pulumi.String("arm64")},
		Code:          pulumi.NewAssetArchive(archive),
		MemorySize:    pulumi.Int(s.MemoryMB),
		Timeout:       pulumi.Int(s.TimeoutSeconds),
		Layers:        layers,
		LoggingConfig: &lambda.FunctionLoggingConfigArgs{
			LogFormat: pulumi.String("Text"), LogGroup: logs.Name,
		},
		Tags: tags,
		// No VpcConfig: the functions run outside a VPC (decision P-INF-99). They
		// reach S3, DynamoDB, SQS and KMS over the public regional endpoints with
		// the role's credentials, and the OTLP door over the internet.
	}
	if len(env) > 0 {
		args.Environment = &lambda.FunctionEnvironmentArgs{Variables: env}
	}
	fn, err := lambda.NewFunction(ctx, s.Name, args, append([]pulumi.ResourceOption{pulumi.DependsOn([]pulumi.Resource{logs})}, opts...)...)
	if err != nil {
		return nil, nil, err
	}
	return fn, logs, nil
}

// writerFiles is the writer's package beside the binary: its configuration,
// the profile document, and the catalogues.
func writerFiles(name string, a *Args) (map[string]string, error) {
	cfg, err := writerConfig(name, a)
	if err != nil {
		return nil, err
	}
	files := map[string]string{configFile: string(cfg), deploymentFile: a.Writer.DeploymentYAML}
	for file, body := range a.Writer.Catalogues {
		if strings.ContainsAny(file, "/\\") || !strings.HasPrefix(file, "catalogue") || !strings.HasSuffix(file, ".yaml") {
			return nil, fmt.Errorf("auditpulumi: Writer.Catalogues has %q: a catalogue file is named catalogue.yaml or catalogue-<name>.yaml, "+
				"which is what the writer looks for", file)
		}
		files[cataloguesDir+"/"+file] = body
	}
	return files, nil
}

func notaryFiles(name string, a *Args) (map[string]string, error) {
	cfg, err := notaryConfig(name, a)
	if err != nil {
		return nil, err
	}
	return map[string]string{configFile: string(cfg)}, nil
}

// newSchedule invokes the notary on a schedule, with a role of its own that may
// invoke that one function. It does not retry: a run that failed is the Errors
// alarm's, and the next run seals whatever is missing, because a run is
// idempotent.
func newSchedule(ctx *pulumi.Context, name string, a *Args, fn *lambda.Function, tags pulumi.StringMap,
	opts ...pulumi.ResourceOption) (*scheduler.Schedule, error) {
	role, err := newRole(ctx, name+"-scheduler", a.RolePath, assumeRoleJSON("scheduler.amazonaws.com"), tags, opts...)
	if err != nil {
		return nil, err
	}
	if _, err := iam.NewRolePolicy(ctx, name+"-scheduler", &iam.RolePolicyArgs{
		Role:   role.Name,
		Policy: fn.Arn.ApplyT(invokePolicy).(pulumi.StringOutput),
	}, opts...); err != nil {
		return nil, err
	}
	if _, err := lambda.NewFunctionEventInvokeConfig(ctx, name+"-notary", &lambda.FunctionEventInvokeConfigArgs{
		FunctionName: fn.Name, MaximumRetryAttempts: pulumi.Int(0),
	}, opts...); err != nil {
		return nil, err
	}
	return scheduler.NewSchedule(ctx, name+"-notary", &scheduler.ScheduleArgs{
		Name:                       pulumi.String(name + "-notary"),
		Description:                pulumi.String("Seals the closed hours of the archive."),
		ScheduleExpression:         pulumi.String(a.Notary.Schedule),
		ScheduleExpressionTimezone: pulumi.String("UTC"),
		FlexibleTimeWindow:         &scheduler.ScheduleFlexibleTimeWindowArgs{Mode: pulumi.String("OFF")},
		Target: &scheduler.ScheduleTargetArgs{
			Arn:     fn.Arn,
			RoleArn: role.Arn,
			Input:   pulumi.String("{}"),
			RetryPolicy: &scheduler.ScheduleTargetRetryPolicyArgs{
				MaximumRetryAttempts: pulumi.Int(0), MaximumEventAgeInSeconds: pulumi.Int(3600),
			},
		},
	}, opts...)
}

// newObserveReader is the role audit-observe, in another account, assumes to
// follow the archive.
func newObserveReader(ctx *pulumi.Context, name string, a *Args, bucket *s3.Bucket, key *kms.Key, tags pulumi.StringMap,
	opts ...pulumi.ResourceOption) (*iam.Role, error) {
	o := a.Observe
	role, err := iam.NewRole(ctx, name+"-observe-reader", &iam.RoleArgs{
		Name: pulumi.String(name + "-observe-reader"), Path: pulumi.String(a.RolePath), Tags: tags,
		AssumeRolePolicy: o.TrustedPrincipalArn.ToStringOutput().ApplyT(func(p string) string {
			return trustPolicy(p, o.ExternalID)
		}).(pulumi.StringOutput),
	}, opts...)
	if err != nil {
		return nil, err
	}
	if _, err := iam.NewRolePolicy(ctx, name+"-observe-reader", &iam.RolePolicyArgs{
		Role: role.Name,
		Policy: pulumi.All(bucket.Arn, key.Arn).ApplyT(func(v []any) string {
			return observeReaderPolicy(v[0].(string), v[1].(string))
		}).(pulumi.StringOutput),
	}, opts...); err != nil {
		return nil, err
	}
	return role, nil
}
