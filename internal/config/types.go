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

	// Sink is the writer a process records through.
	Sink struct {
		URL       string `json:"url"`
		TokenFile string `json:"tokenFile,omitempty"`
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

	// Roll is how much a writer gathers from the stream before it writes it.
	Roll struct {
		Interval   Duration `json:"interval,omitzero"`
		MaxRecords int      `json:"maxRecords,omitempty"`
	}
)

// Writer is the configuration of audit-writer: the front door and the write
// path, in one process or in two (mode).
type Writer struct {
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
	ForgetIdentities bool    `json:"forgetIdentities,omitempty"`
	Stream           *Stream `json:"stream,omitempty"`
	Roll             Roll    `json:"roll,omitzero"`
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
	Listen     Listen    `json:"listen,omitzero"`
	Searcher   string    `json:"searcher,omitempty"`
	Database   *Postgres `json:"database,omitempty"`
	Grants     string    `json:"grants"`
	Deployment string    `json:"deployment,omitempty"`
	Sink       Sink      `json:"sink"`
	Archive    *Archive  `json:"archive,omitempty"`
	Exports    *Exports  `json:"exports,omitempty"`
	Keys       *Keys     `json:"keys,omitempty"`
}

// KeyFile is a PEM private key the digests are signed with.
type KeyFile struct {
	Path string `json:"path"`
	ID   string `json:"id,omitempty"`
}

// TransitSigner is an OpenBAO transit ed25519 key.
type TransitSigner struct {
	Key     string  `json:"key"`
	OpenBAO OpenBAO `json:"openbao"`
}

// Signer is what the digests are signed with: exactly one of its fields.
type Signer struct {
	KeyFile *KeyFile       `json:"keyFile,omitempty"`
	KMSKey  string         `json:"kmsKey,omitempty"`
	Transit *TransitSigner `json:"transit,omitempty"`
}

// Digest is the configuration of `audit digest`.
type Digest struct {
	Deployment string   `json:"deployment"`
	Archive    Archive  `json:"archive"`
	Sink       *Sink    `json:"sink,omitempty"`
	Signer     Signer   `json:"signer"`
	Lookback   Duration `json:"lookback,omitzero"`
	MaxWindows int      `json:"maxWindows,omitempty"`
}

// Verify is the configuration of `audit verify`.
type Verify struct {
	Deployment    string   `json:"deployment"`
	Archive       Archive  `json:"archive"`
	Sink          *Sink    `json:"sink,omitempty"`
	PublicKeyFile string   `json:"publicKeyFile"`
	Profiles      []string `json:"profiles,omitempty"`
	Last          Duration `json:"last,omitzero"`
	Lookback      Duration `json:"lookback,omitzero"`
	Record        bool     `json:"record,omitempty"`
}

// Purge is the configuration of `audit purge`.
type Purge struct {
	Deployment       string   `json:"deployment"`
	Database         Postgres `json:"database"`
	IdentifyingAfter Duration `json:"identifyingAfter,omitzero"`
	DedupeWindow     Duration `json:"dedupeWindow,omitzero"`
}

// ClockSync is the configuration of `audit clock-sync`.
type ClockSync struct {
	NTP       []string  `json:"ntp"`
	Sink      *Sink     `json:"sink,omitempty"`
	MaxOffset *Duration `json:"maxOffset,omitempty"`
	Timeout   Duration  `json:"timeout,omitzero"`
}

// Migrate is the configuration of `audit migrate`.
type Migrate struct {
	Database Postgres `json:"database"`
	Reader   string   `json:"reader,omitempty"`
}
