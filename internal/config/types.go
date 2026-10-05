// Package config is what each binary of this repository is configured with:
// one typed configuration per binary, read from one file and validated against
// a JSON Schema before anything starts.
//
// The file is the whole of the configuration. Secrets are the one thing the
// environment adds, and only the ones the file names: a field that holds a
// secret holds the NAME of the environment variable, never a value, and the
// process reads exactly the variables the file names. Telemetry is not here at
// all; it is OpenTelemetry's own environment (OTEL_*).
//
// The types are written by hand and the schemas are generated from schema.go
// into schemas/config/, which is committed; a test holds the two files to one
// another, and a second holds each type to its schema. The decisions are
// docs/decisions/0021-one-validated-configuration-file.md and, for the shared
// rules, truvity/policy 0002 and 0006.
package config

import (
	"encoding/json"
	"fmt"
	"time"
)

// Duration is a time span as the file spells it: a Go duration string such as
// "30s", "2m" or "168h".
type Duration time.Duration

// D returns the span as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// UnmarshalJSON reads a duration string.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("a duration is a string such as \"30s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// MarshalJSON writes a duration string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// The version of a configuration's shape that this build reads is
// `truvity.github.io/<kind>/v1` (see KindName), which is what an absent
// apiVersion means. truvity/policy's LoadKind chooses by it: another version, or
// another kind of document, is refused by name, so a later shape arrives by a
// version and not by a file that quietly means something else.

// Header is what every configuration file carries beside its own keys: the
// version of its shape, and what the loader learned about the file it read.
type Header struct {
	// APIVersion is `truvity.github.io/<kind>/v1`, or absent, which means the same.
	APIVersion string `json:"apiVersion,omitempty"`
	// Source is where the file was and what it held, set by the loader and never
	// part of the file. The writer puts it in its own start-up record.
	Source Source `json:"-"`
}

// Source is the file a configuration was read from.
type Source struct {
	// File is the path it was read from.
	File string
	// Digest is `sha256:` and the SHA-256 of the file's bytes, which is what
	// `sha256sum` prints for it.
	Digest string
}

func (h *Header) setSource(s Source) { h.Source = s }

type (
	// Listen is a TCP listener.
	Listen struct {
		Address string `json:"address"`
	}

	// Postgres is a connection, with the password named rather than carried.
	// It is the shared shape of truvity/policy's fragments/postgres.json.
	Postgres struct {
		URL            string `json:"url"`
		PasswordEnv    string `json:"passwordEnv,omitempty"`
		MaxConnections int    `json:"maxConnections,omitempty"`
	}

	// NATS is a connection to the stream's broker and nothing else. It is the
	// shared shape of truvity/policy's fragments/nats.json.
	NATS struct {
		URL       string `json:"url"`
		TokenFile string `json:"tokenFile,omitempty"`
	}

	// CredentialsEnv names the variables that hold an object store's static
	// credentials.
	CredentialsEnv struct {
		AccessKeyID     string `json:"accessKeyID"`
		SecretAccessKey string `json:"secretAccessKey"`
	}

	// Bucket is an object store addressed by the S3 API. It is the shared
	// shape of truvity/policy's fragments/bucket.json.
	Bucket struct {
		Name           string          `json:"name"`
		Region         string          `json:"region,omitempty"`
		Endpoint       string          `json:"endpoint,omitempty"`
		CA             string          `json:"ca,omitempty"`
		PathStyle      bool            `json:"pathStyle,omitempty"`
		CredentialsEnv *CredentialsEnv `json:"credentialsEnv,omitempty"`
	}

	// Archive is where the archive is and how it is written. The lock mode is a
	// property of what is written, so the query service, which only reads,
	// takes none; and only the writer encrypts what it writes.
	Archive struct {
		Bucket   Bucket `json:"bucket"`
		Prefix   string `json:"prefix,omitempty"`
		LockMode string `json:"lockMode,omitempty"`
		KMSKey   string `json:"kmsKey,omitempty"`
	}

	// Sink is the writer a process records through. Expect says what the
	// writer at that URL is configured to give (logged, queued or archived):
	// a client cannot know it, so the file says, and the process's `require`
	// is checked against it at start-up.
	//
	// Exactly one of URL and SQS names it. SQS is the ingest queue of a writer
	// that runs elsewhere (the writer Lambda): a record sent there is
	// acknowledged `queued`, and the pod's own identity is the credential.
	Sink struct {
		URL       string `json:"url,omitempty"`
		TokenFile string `json:"tokenFile,omitempty"`
		Expect    string `json:"expect,omitempty"`
		SQS       *SQS   `json:"sqs,omitempty"`
	}

	// OpenBAOLogin is a JWT login: the pod's projected service-account token,
	// presented to an auth mount.
	OpenBAOLogin struct {
		Mount   string `json:"mount"`
		Role    string `json:"role"`
		JWTFile string `json:"jwtFile"`
	}

	// OpenBAO is how a process reaches an OpenBAO transit engine: where it is,
	// and exactly one way of signing in.
	OpenBAO struct {
		Address   string        `json:"address"`
		Mount     string        `json:"mount,omitempty"`
		Namespace string        `json:"namespace,omitempty"`
		CAFile    string        `json:"caFile,omitempty"`
		Login     *OpenBAOLogin `json:"login,omitempty"`
		TokenFile string        `json:"tokenFile,omitempty"`
		TokenEnv  string        `json:"tokenEnv,omitempty"`
	}

	// LocalKeys is the local key provider: a root the data keys are wrapped
	// under, and the directory they are kept in.
	LocalKeys struct {
		RootFile string `json:"rootFile"`
		Dir      string `json:"dir,omitempty"`
	}

	// TransitKeys is the transit key provider.
	TransitKeys struct {
		Prefix  string  `json:"prefix,omitempty"`
		OpenBAO OpenBAO `json:"openbao"`
	}

	// Keys is where pseudonymisation keys live. Without it, or with provider
	// none, there are no pseudonyms and no resolve.
	Keys struct {
		Provider string       `json:"provider"`
		Local    *LocalKeys   `json:"local,omitempty"`
		Transit  *TransitKeys `json:"transit,omitempty"`
	}

	// Stream is how a writer or a receiver reaches the wide stream.
	Stream struct {
		NATS     NATS     `json:"nats"`
		Name     string   `json:"name,omitempty"`
		Consumer string   `json:"consumer,omitempty"`
		Batch    int      `json:"batch,omitempty"`
		AckWait  Duration `json:"ackWait,omitzero"`
	}

	// SQS is an Amazon SQS queue. There are no credentials here: they are the
	// SDK's ambient ones, which on Kubernetes is the pod's workload identity
	// (EKS Pod Identity or IRSA, bound through the service account).
	SQS struct {
		QueueURL string `json:"queueUrl"`
		Region   string `json:"region,omitempty"`
		FIFO     bool   `json:"fifo,omitempty"`
	}

	// ConsumeSQS is a queue a writer consumes, with the two knobs of a consumer.
	ConsumeSQS struct {
		SQS
		Batch      int      `json:"batch,omitempty"`
		Visibility Duration `json:"visibility,omitzero"`
	}

	// Log is the log sink, which has nothing to configure: it writes a line per
	// record to standard output.
	Log struct{}

	// Forward is where a receiver sends what it took: exactly one of its
	// fields.
	Forward struct {
		NATS *Stream `json:"nats,omitempty"`
		SQS  *SQS    `json:"sqs,omitempty"`
		Log  *Log    `json:"log,omitempty"`
	}

	// Consume is what a writer reads its records from, beside its own sink:
	// exactly one of its fields.
	Consume struct {
		NATS *Stream     `json:"nats,omitempty"`
		SQS  *ConsumeSQS `json:"sqs,omitempty"`
	}

	// Roll is how much a writer gathers from the stream before it writes it.
	Roll struct {
		Interval   Duration `json:"interval,omitzero"`
		MaxRecords int      `json:"maxRecords,omitempty"`
	}
)

// Writer is the configuration of audit-writer: the front door and the write
// path, in one process or in two (mode).
type Writer struct {
	Header
	Mode            string    `json:"mode,omitempty"`
	Listen          Listen    `json:"listen,omitzero"`
	Deployment      string    `json:"deployment"`
	Workloads       string    `json:"workloads,omitempty"`
	AnonymousWrites bool      `json:"anonymousWrites,omitempty"`
	Catalogues      string    `json:"catalogues,omitempty"`
	Archive         *Archive  `json:"archive,omitempty"`
	Database        *Postgres `json:"database,omitempty"`
	Replicas        int       `json:"replicas,omitempty"`
	Keys            *Keys     `json:"keys,omitempty"`
	// ForgetIdentities turns off keeping the identity behind each pseudonym,
	// sealed under its key: the default keeps it, so that resolve can find it.
	ForgetIdentities bool `json:"forgetIdentities,omitempty"`
	// Stream is the NATS shorthand: a receiver's forward.nats, or a writer's
	// consume.nats. It is kept so that a file written before the two existed
	// reads as it did; finish() carries it into Forward or Consume.
	Stream  *Stream  `json:"stream,omitempty"`
	Forward *Forward `json:"forward,omitempty"`
	Consume *Consume `json:"consume,omitempty"`
	// Require is the weakest durability the chain may give. Unset is archived
	// for a writer and queued for a receiver (see Writer.finish).
	Require string `json:"require,omitempty"`
	Roll    Roll   `json:"roll,omitzero"`
}

// Exports is where the query service puts what it exports: a bucket of its
// own, with no Object Lock, which clears it.
type Exports struct {
	Bucket    Bucket   `json:"bucket"`
	Expiry    Duration `json:"expiry,omitzero"`
	LinkValid Duration `json:"linkValid,omitzero"`
}

// Query is the configuration of audit-query.
type Query struct {
	Header
	Listen     Listen    `json:"listen,omitzero"`
	Searcher   string    `json:"searcher,omitempty"`
	Database   *Postgres `json:"database,omitempty"`
	Grants     string    `json:"grants"`
	Deployment string    `json:"deployment,omitempty"`
	Sink       Sink      `json:"sink"`
	Require    string    `json:"require,omitempty"`
	Archive    *Archive  `json:"archive,omitempty"`
	Exports    *Exports  `json:"exports,omitempty"`
	Keys       *Keys     `json:"keys,omitempty"`
}

// WakeNATS is a subject of bucket notifications on a NATS server.
type WakeNATS struct {
	NATS    NATS   `json:"nats"`
	Subject string `json:"subject"`
}

// Wake is what makes observe look sooner than its next poll: exactly one of its
// fields. What it carries is never read, so a lost notification costs latency.
type Wake struct {
	NATS *WakeNATS `json:"nats,omitempty"`
	SQS  *SQS      `json:"sqs,omitempty"`
}

// Observe is the configuration of audit-observe: the indexer, which follows the
// archive by cursor and writes the index.
type Observe struct {
	Header
	Listen   Listen   `json:"listen,omitzero"`
	Archive  Archive  `json:"archive"`
	Database Postgres `json:"database"`
	// Settle keeps the cursor this far behind now: longer than a put can take,
	// and than the clocks involved can disagree.
	Settle Duration `json:"settle,omitzero"`
	// Interval is the poll.
	Interval Duration `json:"interval,omitzero"`
	// Batch is how many rows are written in one transaction.
	Batch int `json:"batch,omitempty"`
	// Profiles limits what is followed; unset follows every profile.
	Profiles []string `json:"profiles,omitempty"`
	Wake     *Wake    `json:"wake,omitempty"`
}

// Seals is what `audit verify` is given to check seals with: the roots it
// pins, and how late a seal may be before its absence is a finding.
type Seals struct {
	// Roots are the RFC 7638 thumbprints of the root keys this verifier trusts.
	// Nothing else in the bucket is.
	Roots []string `json:"roots"`
	// Settle is the notary's settle window: an hour is sealable once it has
	// ended and this long has passed.
	Settle Duration `json:"settle,omitzero"`
	// Grace is how long after that a seal may still be missing before it is
	// reported: the notary runs hourly, so a seal is up to an hour behind.
	Grace Duration `json:"grace,omitzero"`
}

// Verify is the configuration of `audit verify`.
type Verify struct {
	Header
	Deployment string   `json:"deployment"`
	Archive    Archive  `json:"archive"`
	Seals      *Seals   `json:"seals,omitempty"`
	Sink       *Sink    `json:"sink,omitempty"`
	Require    string   `json:"require,omitempty"`
	Profiles   []string `json:"profiles,omitempty"`
	Last       Duration `json:"last,omitzero"`
}

type (
	// KMSSigner is an AWS KMS key that signs seals: ECC_NIST_P384, SIGN_VERIFY.
	KMSSigner struct {
		Key    string `json:"key"`
		Region string `json:"region,omitempty"`
	}

	// TransitSigner is an OpenBAO transit key that signs seals: ecdsa-p384.
	TransitSigner struct {
		Key     string  `json:"key"`
		OpenBAO OpenBAO `json:"openbao"`
	}

	// FileSigner is a P-384 private key in a PEM file, for development and for
	// a deployment small enough to accept that the key lives beside the archive.
	FileSigner struct {
		Path string `json:"path"`
	}

	// Signer is where the notary's key is: exactly one of its fields.
	Signer struct {
		KMS     *KMSSigner     `json:"kms,omitempty"`
		Transit *TransitSigner `json:"transit,omitempty"`
		File    *FileSigner    `json:"file,omitempty"`
	}
)

// Notary is the configuration of `audit-notary`: the archive it reads and puts
// seals in, and the key it signs them with.
type Notary struct {
	Header
	Archive Archive `json:"archive"`
	Signer  Signer  `json:"signer"`
	// Profiles to seal; unset is every profile the archive has records for.
	Profiles []string `json:"profiles,omitempty"`
	// Settle is how long after an hour has ended it is sealed, so that a batch
	// put late in the hour it is keyed by is in the seal.
	Settle Duration `json:"settle,omitzero"`
	Sink   *Sink    `json:"sink,omitempty"`
	// Require is the weakest durability the notary's own records may be
	// acknowledged with.
	Require string `json:"require,omitempty"`
}

// Purge is the configuration of `audit purge`.
type Purge struct {
	Header
	Deployment       string   `json:"deployment"`
	Database         Postgres `json:"database"`
	IdentifyingAfter Duration `json:"identifyingAfter,omitzero"`
	DedupeWindow     Duration `json:"dedupeWindow,omitzero"`
}

// ClockSync is the configuration of `audit clock-sync`.
type ClockSync struct {
	Header
	NTP       []string  `json:"ntp"`
	Sink      *Sink     `json:"sink,omitempty"`
	Require   string    `json:"require,omitempty"`
	MaxOffset *Duration `json:"maxOffset,omitempty"`
	Timeout   Duration  `json:"timeout,omitzero"`
}

// Migrate is the configuration of `audit migrate`.
type Migrate struct {
	Header
	Database Postgres `json:"database"`
	// Roles name the database roles of the parts, each granted what its part
	// needs and nothing else. Reader is the query service's; Writer, Observe and
	// Purge are the write path's, the indexer's and the retention job's.
	Reader  string `json:"reader,omitempty"`
	Writer  string `json:"writer,omitempty"`
	Observe string `json:"observe,omitempty"`
	Purge   string `json:"purge,omitempty"`
}

type (
	// DynamoDB is a DynamoDB table the writer keeps its deduplication in. The
	// credentials are the SDK's ambient ones: on Lambda, the function's role.
	DynamoDB struct {
		Table  string `json:"table"`
		Region string `json:"region,omitempty"`
		// Window is how long a written record's id is remembered. Unset is
		// the widest window the profiles ask for.
		Window Duration `json:"window,omitzero"`
	}

	// Dedupe is where a writer without a database keeps what it has written:
	// exactly one of its fields.
	Dedupe struct {
		DynamoDB *DynamoDB `json:"dynamodb,omitempty"`
	}
)

// WriterLambda is the configuration of audit-writer-lambda: the write path as an
// AWS Lambda behind an SQS event source mapping. There is no listener, no
// stream, no registry and no database: the batch is the SQS event, and what the
// Postgres table does for a writer on Kubernetes is a DynamoDB table.
type WriterLambda struct {
	Header
	Deployment       string  `json:"deployment"`
	Catalogues       string  `json:"catalogues,omitempty"`
	Archive          Archive `json:"archive"`
	Keys             *Keys   `json:"keys,omitempty"`
	ForgetIdentities bool    `json:"forgetIdentities,omitempty"`
	Dedupe           Dedupe  `json:"dedupe"`
	Require          string  `json:"require,omitempty"`
}
