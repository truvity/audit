package auditpulumi

import (
	"encoding/json"
)

// A policy document is built from ARNs that exist only once the resources do, so
// each builder takes them as strings and returns the JSON; audit.go calls them
// inside an Apply. Keeping them pure is what lets the tests read the statements
// the roles get.

type statement map[string]any

func policyJSON(statements ...statement) string {
	b, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": statements})
	if err != nil {
		panic(err) // maps of strings
	}
	return string(b)
}

func allow(actions []string, resources []string, condition map[string]any) statement {
	s := statement{"Effect": "Allow", "Action": actions, "Resource": resources}
	if condition != nil {
		s["Condition"] = condition
	}
	return s
}

func under(bucketArn string, prefixes ...string) []string {
	out := make([]string, len(prefixes))
	for i, p := range prefixes {
		out[i] = bucketArn + "/" + p + "*"
	}
	return out
}

// The prefixes of the bucket contract (docs/reference/bucket-contract.md) a part
// writes. The writer writes everything but seals and keys; the notary writes
// those two and nothing else.
var (
	writerPrefixes = []string{"records/", "catalogue/", "schema/", "identity/", "dlq/"}
	sealPrefixes   = []string{"seals/", "keys/"}
)

// assumeRoleJSON lets a service principal assume a role.
func assumeRoleJSON(service string) string {
	return policyJSON(statement{
		"Effect": "Allow", "Principal": map[string]any{"Service": service}, "Action": "sts:AssumeRole",
	})
}

// logsStatement is the function's own log group, and nothing else.
func logsStatement(logGroupArn string) statement {
	return allow([]string{"logs:CreateLogStream", "logs:PutLogEvents"}, []string{logGroupArn + ":*"}, nil)
}

// webIdentityStatement lets a function role ask STS for the identity token the
// OTLP extension exchanges. sts:IdentityTokenAudience is a multi-valued key (the
// API takes a list of audiences), so it needs ForAllValues:StringEquals: a plain
// StringEquals is an implicit deny when the request carries the audience as a
// list. ForAllValues also passes on an empty set, which is safe here only
// because Audience is a required parameter of GetWebIdentityToken. The signing
// algorithm and the lifetime are pinned to what the extension asks for.
func webIdentityStatement(audience string) statement {
	return allow([]string{"sts:GetWebIdentityToken"}, []string{"*"}, map[string]any{
		"ForAllValues:StringEquals": map[string]any{"sts:IdentityTokenAudience": []string{audience}},
		"StringEquals":              map[string]any{"sts:SigningAlgorithm": "ES384"},
		"NumericLessThanEquals":     map[string]any{"sts:DurationSeconds": "300"},
	})
}

// writerPolicy is what the writer function may do, and no more.
//
// PutObjectRetention and PutObjectLegalHold are needed by PutObject itself: S3
// refuses a put that carries an Object Lock header unless the caller also holds
// the matching permission. The writer reads the catalogue it compares at
// start-up, the legal holds, and the archive an addendum scans; it lists the
// bucket for the last two. It has no delete, no access to seals/ or keys/, and
// no KMS Sign: whoever can write the archive and can also sign for it can choose
// what to sign (ADR 0019). When its configuration names secrets it may read the
// SSM parameters under its root and decrypt them, and read nothing else of SSM. With no Object Lock (locked false) the writer sends
// no lock header, so it is granted neither permission.
func writerPolicy(bucketArn, archiveKeyArn, tableArn, queueArn, logGroupArn string, audience string, locked bool,
	secrets *secretGrant) string {
	put := []string{"s3:PutObject"}
	if locked {
		put = append(put, "s3:PutObjectRetention", "s3:PutObjectLegalHold")
	}
	st := []statement{
		allow(put, under(bucketArn, writerPrefixes...), nil),
		allow([]string{"s3:GetObject"}, under(bucketArn, append(append([]string{}, writerPrefixes...), "holds/")...), nil),
		allow([]string{"s3:ListBucket"}, []string{bucketArn}, nil),
		allow([]string{"dynamodb:GetItem", "dynamodb:BatchGetItem", "dynamodb:PutItem"}, []string{tableArn}, nil),
		allow([]string{"sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:GetQueueAttributes", "sqs:ChangeMessageVisibility"},
			[]string{queueArn}, nil),
		logsStatement(logGroupArn),
	}
	st = append(st, archiveKeyStatements(archiveKeyArn, "kms:GenerateDataKey", "kms:Decrypt")...)
	st = append(st, secrets.statements()...)
	if audience != "" {
		st = append(st, webIdentityStatement(audience))
	}
	return policyJSON(st...)
}

// archiveKeyStatements is the grant on the archive key, and nothing when there is
// no key (Archive.Encryption "s3": the empty ARN).
func archiveKeyStatements(archiveKeyArn string, actions ...string) []statement {
	if archiveKeyArn == "" {
		return nil
	}
	return []statement{allow(actions, []string{archiveKeyArn}, nil)}
}

// notaryPolicy is what the notary function may do: read the records it seals and
// the seals it chains to, put seals and keys/roots.jwks, and sign with the seal
// key and with nothing else. It cannot put a record, which is what makes a
// compromised writer unable to seal what it wrote.
func notaryPolicy(bucketArn, archiveKeyArn, sealKeyArn, logGroupArn string, audience string, locked bool) string {
	put := []string{"s3:PutObject"}
	if locked {
		put = append(put, "s3:PutObjectRetention")
	}
	st := []statement{
		allow([]string{"s3:GetObject"}, under(bucketArn, "records/", "seals/", "keys/"), nil),
		allow([]string{"s3:ListBucket"}, []string{bucketArn}, nil),
		allow(put, under(bucketArn, sealPrefixes...), nil),
		allow([]string{"kms:Sign", "kms:GetPublicKey", "kms:DescribeKey"}, []string{sealKeyArn}, nil),
		logsStatement(logGroupArn),
	}
	st = append(st, archiveKeyStatements(archiveKeyArn, "kms:GenerateDataKey", "kms:Decrypt")...)
	if audience != "" {
		st = append(st, webIdentityStatement(audience))
	}
	return policyJSON(st...)
}

// observeReaderPolicy is what audit-observe reads the archive with, across the
// accounts or from a cluster: list and get on records/, catalogue/, schema/,
// seals/ and keys/, and decrypt under the archive key when there is one. It
// writes nothing, which is what ADR 0020 means by observe following the bucket.
func observeReaderPolicy(bucketArn, archiveKeyArn string) string {
	prefixes := []string{"records/", "catalogue/", "schema/", "seals/", "keys/"}
	lists := make([]string, len(prefixes))
	for i, p := range prefixes {
		lists[i] = p + "*"
	}
	st := []statement{
		allow([]string{"s3:GetObject"}, under(bucketArn, prefixes...), nil),
		allow([]string{"s3:ListBucket"}, []string{bucketArn}, map[string]any{
			"StringLike": map[string]any{"s3:prefix": lists},
		}),
	}
	st = append(st, archiveKeyStatements(archiveKeyArn, "kms:Decrypt")...)
	return policyJSON(st...)
}

// archiveWriterPolicy is what a workload outside AWS that writes part of the
// archive may do: put under the given prefixes only (with the retention
// permission when the bucket is locked), read what it must chain to or compare
// (records/ and the prefixes it writes), list, and use the archive key. It has no
// delete, no legal hold, no seal key, and no queue or table.
func archiveWriterPolicy(bucketArn, archiveKeyArn string, prefixes []string, locked bool) string {
	put := []string{"s3:PutObject"}
	if locked {
		put = append(put, "s3:PutObjectRetention")
	}
	reads := append([]string{"records/"}, prefixes...)
	st := []statement{
		allow(put, under(bucketArn, prefixes...), nil),
		allow([]string{"s3:GetObject"}, under(bucketArn, dedupe(reads)...), nil),
		allow([]string{"s3:ListBucket"}, []string{bucketArn}, nil),
	}
	st = append(st, archiveKeyStatements(archiveKeyArn, "kms:GenerateDataKey", "kms:Decrypt")...)
	return policyJSON(st...)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// trustPolicy lets the given principal (when there is one) assume a role, with an
// external id when there is one, and the one ServiceAccount of irsa (when there
// is one) assume it by web identity.
func trustPolicy(principalArn, externalID string, irsa *IRSAArgs, providerArn string) string {
	var st []statement
	if principalArn != "" {
		s := statement{"Effect": "Allow", "Principal": map[string]any{"AWS": principalArn}, "Action": "sts:AssumeRole"}
		if externalID != "" {
			s["Condition"] = map[string]any{"StringEquals": map[string]any{"sts:ExternalId": externalID}}
		}
		st = append(st, s)
	}
	if irsa != nil {
		st = append(st, irsaTrustStatement(*irsa, providerArn))
	}
	return policyJSON(st...)
}

// irsaTrustStatement is the web identity trust of one ServiceAccount. BOTH
// conditions matter: without the sub pin any ServiceAccount of the cluster could
// assume the role, and without the aud pin a token minted for another audience
// would be accepted.
func irsaTrustStatement(i IRSAArgs, providerArn string) statement {
	return statement{
		"Effect": "Allow", "Principal": map[string]any{"Federated": providerArn}, "Action": "sts:AssumeRoleWithWebIdentity",
		"Condition": map[string]any{"StringEquals": map[string]any{
			i.IssuerHost + ":aud": i.Audience,
			i.IssuerHost + ":sub": "system:serviceaccount:" + i.Namespace + ":" + i.ServiceAccount,
		}},
	}
}

// invokePolicy is what the scheduler's role may do: invoke the notary.
func invokePolicy(functionArn string) string {
	return policyJSON(allow([]string{"lambda:InvokeFunction"}, []string{functionArn, functionArn + ":*"}, nil))
}

// sealKeyPolicy is the seal key's own policy. The default policy hands the key to
// IAM, so any principal in the account with a kms:Sign allow could sign seals;
// this one does not. The account's root administers the key and cannot use it,
// and only the notary's role signs. (KMS policies are the one place an
// administrator is held out of a key's use, and a key that signs the trail is
// that place.)
func sealKeyPolicy(accountRootArn, notaryRoleArn string) string {
	return policyJSON(
		statement{
			"Sid": "Administer", "Effect": "Allow", "Principal": map[string]any{"AWS": accountRootArn},
			"Action": []string{
				"kms:Create*", "kms:Describe*", "kms:Enable*", "kms:List*", "kms:Put*", "kms:Update*", "kms:Revoke*",
				"kms:Disable*", "kms:Get*", "kms:Delete*", "kms:TagResource", "kms:UntagResource",
				"kms:ScheduleKeyDeletion", "kms:CancelKeyDeletion",
			},
			"Resource": "*",
		},
		statement{
			"Sid": "SealWithTheNotaryOnly", "Effect": "Allow", "Principal": map[string]any{"AWS": notaryRoleArn},
			"Action": []string{"kms:Sign", "kms:GetPublicKey", "kms:DescribeKey"}, "Resource": "*",
		},
	)
}
