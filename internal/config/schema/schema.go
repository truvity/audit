// Package schema builds the JSON Schema of each binary's configuration file.
//
// It is a package of its own, apart from the types and the loader, so that the
// generator which writes the committed files does not need them to exist.
package schema

import (
	"bytes"
	"encoding/json"
)

// BaseID is where the schemas are served: the same site as the record's.
const BaseID = "https://truvity.github.io/audit/schemas/v1/config/"

// The shared shapes this repository takes from truvity/policy, by the `$id`
// they carry. The loader resolves them from its embedded copies.
const policy = "https://github.com/truvity/policy/schemas/"

// Names are the binaries and commands that read a file, as the schema files are
// named: schemas/config/<name>.schema.json.
var Names = []string{
	"audit-writer", "audit-query",
	"audit-digest", "audit-verify", "audit-purge", "audit-clock-sync", "audit-migrate",
}

type m = map[string]any

func ref(id string) m   { return m{"$ref": id} }
func def(name string) m { return ref("#/$defs/" + name) }

func obj(description string, props m, required ...string) m {
	o := m{
		"type":                 "object",
		"additionalProperties": false,
		"description":          description,
		"properties":           props,
	}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}

func str(description string) m {
	return m{"type": "string", "minLength": 1, "description": description}
}

func strDefault(description, def string) m {
	s := str(description)
	s["default"] = def
	return s
}

func integer(description string, min int, def any) m {
	o := m{"type": "integer", "minimum": min, "description": description}
	if def != nil {
		o["default"] = def
	}
	return o
}

func boolean(description string) m {
	return m{"type": "boolean", "description": description}
}

func duration(description string, def string) m {
	o := m{"$ref": "#/$defs/duration", "description": description}
	if def != "" {
		o["default"] = def
	}
	return o
}

// sharedDefs are the shapes more than one schema uses, written once here and
// carried into each schema that names them, because a schema the loader reads
// has to stand alone.
func sharedDefs() map[string]m {
	return map[string]m{
		"duration": {
			"type":        "string",
			"pattern":     `^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`,
			"description": "A Go duration: 30s, 2m, 168h.",
		},
		// A connection URL with a password in it is a secret in the file.
		// The fragment's own pattern lets one through; this does not.
		"postgres": {
			"allOf": []any{
				ref(policy + "fragments/postgres.json"),
				m{"properties": m{"url": m{"not": m{
					"pattern": `^[A-Za-z][A-Za-z0-9+.-]*://[^/?#@]*:[^/?#@]*@`,
				}}}},
			},
			"description": "A PostgreSQL connection. The URL carries no password: a password in it is refused, and `passwordEnv` names the environment variable that holds it.",
		},
		"sink": obj("The writer this process records through.", m{
			"url":       str("The writer's base URL."),
			"tokenFile": str("A file holding the token presented to the writer, read afresh on every request: in a cluster, the pod's projected service-account token. Unset presents none, which an anonymous trial install accepts."),
		}, "url"),
		"openbao": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"description":          "How a process reaches an OpenBAO transit engine: where it is, and exactly one way of signing in.",
			"properties": m{
				"address":   str("The server, for example https://openbao.example:8200."),
				"mount":     strDefault("Where the transit engine is mounted.", "transit"),
				"namespace": str("The OpenBAO namespace the engine and the auth mount are in. Unset is the root namespace."),
				"caFile":    str("A PEM bundle trusted beside the system roots, for a server on a private chain."),
				"login": obj("Sign in with a JWT: the pod's projected service-account token, presented to an auth mount. Nothing is stored.", m{
					"mount":   str("The JWT auth mount, for example jwt-devel."),
					"role":    str("The role on that mount."),
					"jwtFile": str("The file holding the JWT, read at every login because the kubelet replaces a projected token before it expires."),
				}, "mount", "role", "jwtFile"),
				"tokenFile": str("A file holding a token, read on every call, for a token something else keeps renewed."),
				"tokenEnv":  str("The NAME of the environment variable holding a token."),
			},
			"required": []string{"address"},
			"oneOf": []any{
				m{"required": []string{"login"}},
				m{"required": []string{"tokenFile"}},
				m{"required": []string{"tokenEnv"}},
			},
		},
		"keys": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"description":          "Where pseudonymisation keys live. Unset, or `none`, means no pseudonyms, no key material and no resolve.",
			"properties": m{
				"provider": m{"enum": []string{"none", "local", "transit"}, "description": "`none`, `local` (a root and a directory) or `transit` (OpenBAO)."},
				"local": obj("The local provider.", m{
					"rootFile": str("A file holding the 32-byte root the data keys are wrapped under."),
					"dir":      str("Where the wrapped data keys are kept. They are random, not derived, so this directory is the only copy; unset keeps them in memory, which a trial install may do and nothing else should."),
				}, "rootFile"),
				"transit": obj("The transit provider.", m{
					"prefix":  strDefault("What every key's name starts with: <prefix>.<purpose>.<tenant>.", "audit"),
					"openbao": def("openbao"),
				}, "openbao"),
			},
			"required": []string{"provider"},
			"allOf": []any{
				m{"if": m{"properties": m{"provider": m{"const": "local"}}, "required": []string{"provider"}},
					"then": m{"required": []string{"local"}, "properties": m{"transit": false}}},
				m{"if": m{"properties": m{"provider": m{"const": "transit"}}, "required": []string{"provider"}},
					"then": m{"required": []string{"transit"}, "properties": m{"local": false}}},
				m{"if": m{"properties": m{"provider": m{"const": "none"}}, "required": []string{"provider"}},
					"then": m{"properties": m{"local": false, "transit": false}}},
			},
		},
	}
}

// archive is the archive's schema for a command that writes to it, which
// takes a lock mode, and for the writer, which also encrypts.
func archive(writes, encrypts bool) m {
	props := m{
		"bucket": ref(policy + "fragments/bucket.json"),
		"prefix": str("The prefix within the bucket. Required in a bucket shared with other installations: it is what keeps two of them apart."),
	}
	if writes {
		props["lockMode"] = m{
			"enum":        []string{"compliance", "governance", "none"},
			"default":     "compliance",
			"description": "The Object Lock mode every object is written in: compliance (the record tier), governance (a lock a privileged role can shorten), or none (the attested tier, for a store without Object Lock). A process refuses to start when a profile demands a stricter mode than this.",
		}
	}
	if encrypts {
		props["kmsKey"] = str("The key objects are encrypted with. Unset uses the bucket's default encryption, which a deployment should still be setting.")
	}
	return obj("Where the archive is and how it is written.", props, "bucket")
}

func listen() m { return ref(policy + "fragments/listen.json") }

// document builds one schema: its identity, its properties, and the shapes it
// shares.
func document(name, title, description string, props m, required []string, uses []string, extra m) m {
	shared := sharedDefs()
	defs := m{}
	for _, u := range uses {
		defs[u] = shared[u]
	}
	s := m{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"$id":                  BaseID + name + ".schema.json",
		"title":                title,
		"description":          description,
		"type":                 "object",
		"additionalProperties": false,
		"properties":           props,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	if len(defs) > 0 {
		s["$defs"] = defs
	}
	for k, v := range extra {
		s[k] = v
	}
	return s
}

const secretsNote = " Secrets are never in this file: a field named ...Env holds the NAME of the environment variable that holds the secret. Telemetry is the OTEL_* environment, not configuration."

func writerSchema() m {
	props := m{
		"mode": m{"enum": []string{"writer", "receiver"}, "default": "writer",
			"description": "`writer` serves the sink, writes the archive and consumes the stream when there is one. `receiver` serves the sink and publishes to the stream and nothing else: it holds no archive and no keys, because a receiver holding either would be a writer."},
		"listen":          listen(),
		"deployment":      str("Path to the profile configuration: which presets each profile is composed from."),
		"workloads":       str("Path to the file naming the issuers trusted to say which workload is publishing, and which source each speaks for. Exactly one of `workloads` and `anonymousWrites`."),
		"anonymousWrites": m{"const": true, "description": "Accept writes over HTTP from callers nobody verified, stamped with no observer. For a trial install only."},
		"catalogues":      str("A directory of catalogues to register at start-up."),
		"archive":         archive(true, true),
		"database": m{"$ref": "#/$defs/postgres",
			"description": "The index and the shared deduplication table. Without it the writer indexes nothing, deduplicates in process, and may only run one replica."},
		"replicas":         integer("How many writers share this stream. Above one it needs a database, and with local keys a directory every replica shares.", 1, 1),
		"keys":             def("keys"),
		"forgetIdentities": boolean("Do not keep the identity behind each pseudonym, sealed under its key. By default it is kept, so that resolve can find it."),
		"stream": obj("The wide stream. Without it the writer only serves its own sink.", m{
			"nats":     ref(policy + "fragments/nats.json"),
			"name":     strDefault("The stream.", "AUDIT"),
			"consumer": strDefault("The durable consumer this installation's writers share.", "audit-writer"),
			"batch":    integer("How many records are taken from the stream at once.", 1, 100),
			"ackWait":  duration("How long the stream waits for a batch to be taken before offering it again. It must exceed `roll.interval` plus the longest a put can take.", "2m"),
		}, "nats"),
		"roll": obj("How much a writer gathers from the stream before it writes, which decides how many objects a day of records becomes.", m{
			"interval":   duration("How long gathered records wait before they are written, and how long an object stays open within one write.", "30s"),
			"maxRecords": integer("How many gathered records are written at once.", 1, 5000),
		}),
	}
	return document("audit-writer", "audit-writer",
		"The configuration of audit-writer, the installation's front door and write path."+secretsNote,
		props, []string{"deployment"}, []string{"postgres", "openbao", "keys", "duration"},
		m{
			"oneOf": []any{
				m{"required": []string{"workloads"}, "not": m{"required": []string{"anonymousWrites"}}},
				m{"required": []string{"anonymousWrites"}, "not": m{"required": []string{"workloads"}}},
			},
			"allOf": []any{
				m{"if": m{"properties": m{"mode": m{"const": "receiver"}}, "required": []string{"mode"}},
					"then": m{"required": []string{"stream"}, "properties": m{"archive": false, "keys": false, "catalogues": false}},
					"else": m{"required": []string{"archive"}}},
			},
		})
}

func querySchema() m {
	props := m{
		"listen": listen(),
		"searcher": m{"enum": []string{"postgres", "s3scan"}, "default": "postgres",
			"description": "Where answers come from: `postgres` (the index), or `s3scan` (the archive, within a budget) for a deployment with no database."},
		"database": m{"$ref": "#/$defs/postgres",
			"description": "The index, as a role that does NOT own the tables: tenant row-level security binds only a non-owner."},
		"grants":     str("Path to the file naming the trusted issuers and mapping their claims to grants."),
		"deployment": str("Path to the profile configuration, which a grant preset turns roles into profiles with."),
		"sink":       def("sink"),
		"archive": func() m {
			a := archive(false, false)
			a["description"] = "The archive the records are in: where a record's standing in the digest chain is read for Get, and what the s3scan searcher reads. Without it Get still answers, with where the copy is and nothing about whether it has been verified."
			return a
		}(),
		"exports": obj("Where exports go: a bucket of its own with no Object Lock, which clears them. Without it the export operation is refused.", m{
			"bucket":    ref(policy + "fragments/bucket.json"),
			"expiry":    duration("How long an export is kept before the bucket clears it.", "168h"),
			"linkValid": duration("How long a download link works.", "1h"),
		}, "bucket"),
		"keys": m{"$ref": "#/$defs/keys",
			"description": "The writer's key provider, which turns resolve on: a query service without the keys cannot undo a pseudonym whatever a grant says. It needs `archive`."},
	}
	return document("audit-query", "audit-query",
		"The configuration of audit-query, the read path."+secretsNote,
		props, []string{"grants", "sink"}, []string{"postgres", "sink", "openbao", "keys", "duration"},
		m{"allOf": []any{
			m{"if": m{"properties": m{"searcher": m{"const": "s3scan"}}, "required": []string{"searcher"}},
				"then": m{"required": []string{"archive"}},
				"else": m{"required": []string{"database"}}},
		}})
}

func digestSchema() m {
	props := m{
		"deployment": str("Path to the profile configuration."),
		"archive":    archive(true, false),
		"sink":       m{"$ref": "#/$defs/sink", "description": "The writer this job records what it sealed through (audit.digest.written)."},
		"signer": obj("What the digests are signed with: exactly one. An unsigned chain proves nothing, and one chain has one signer.", m{
			"keyFile": obj("A PEM ed25519 private key.", m{
				"path": str("The key file."),
				"id":   str("The name a digest records the signing key under."),
			}, "path"),
			"kmsKey": str("An AWS KMS ECC_NIST_P256 key. The private half never leaves KMS."),
			"transit": obj("An OpenBAO transit ed25519 key, signed in to as the job's own identity and never the writer's.", m{
				"key":     str("The transit key's name."),
				"openbao": def("openbao"),
			}, "key", "openbao"),
		}),
		"lookback":   duration("How far before a window to look for objects keyed under an older day. The verify job's must be at least this.", ""),
		"maxWindows": integer("How many windows one run may seal when catching up.", 1, nil),
	}
	s := document("audit-digest", "audit digest",
		"The configuration of `audit digest --config`, which seals windows into the signed chain."+secretsNote,
		props, []string{"deployment", "archive", "signer"}, []string{"sink", "openbao", "duration"}, nil)
	s["properties"].(m)["signer"].(m)["oneOf"] = []any{
		m{"required": []string{"keyFile"}}, m{"required": []string{"kmsKey"}}, m{"required": []string{"transit"}},
	}
	return s
}

func verifySchema() m {
	props := m{
		"deployment":    str("Path to the profile configuration; the check holds each object's lock to what its profile demands."),
		"archive":       archive(true, false),
		"sink":          m{"$ref": "#/$defs/sink", "description": "The writer this job records what it checked through."},
		"publicKeyFile": str("The PEM public key the digests were signed with."),
		"profiles":      m{"type": "array", "minItems": 1, "uniqueItems": true, "items": str("A profile name."), "description": "The profiles whose chains to walk. Unset walks every profile the deployment composes."},
		"last":          duration("Check the windows of the last this long, ending at the hour that has closed.", "24h"),
		"lookback":      duration("How far before the range to look for objects keyed under an older day; at least what the digest job used.", ""),
		"record":        boolean("Write a verification per window into the archive under verified/, which a record's provenance reads. Needs write access there."),
	}
	return document("audit-verify", "audit verify",
		"The configuration of `audit verify --config`, which walks digest chains and reports what it finds."+secretsNote,
		props, []string{"deployment", "archive", "publicKeyFile"}, []string{"sink", "duration"}, nil)
}

func purgeSchema() m {
	props := m{
		"deployment":       str("Path to the profile configuration."),
		"database":         def("postgres"),
		"identifyingAfter": duration("How long the index keeps who an event happened to, as opposed to what happened. Unset forgets nothing early: no shipped preset states a schedule, so the number is a deployment's own policy.", ""),
		"dedupeWindow":     duration("How long a written identifier is remembered. Unset is the widest window the profiles ask for.", ""),
	}
	return document("audit-purge", "audit purge",
		"The configuration of `audit purge --config`, which brings the index and the deduplication table within the profiles."+secretsNote,
		props, []string{"deployment", "database"}, []string{"postgres", "duration"}, nil)
}

func clockSyncSchema() m {
	props := m{
		"ntp":       m{"type": "array", "minItems": 1, "items": str("A time reference, host or host:port."), "description": "Time references; the quickest to answer is believed, and one being unreachable is survivable."},
		"sink":      m{"$ref": "#/$defs/sink", "description": "The writer the reading is recorded through."},
		"maxOffset": duration("How far the clock may be out before the run fails; 0s accepts any offset and only records it.", "1s"),
		"timeout":   duration("How long to wait for a reference.", "5s"),
	}
	return document("audit-clock-sync", "audit clock-sync",
		"The configuration of `audit clock-sync --config`, which compares the clock with UTC and records the answer."+secretsNote,
		props, []string{"ntp"}, []string{"sink", "duration"}, nil)
}

func migrateSchema() m {
	props := m{
		"database": def("postgres"),
		"reader":   str("A role to grant what the query service needs: usage on the schema and select on its tables, now and later, and nothing else. The role must already exist."),
	}
	return document("audit-migrate", "audit migrate",
		"The configuration of `audit migrate --config`, which applies the index schema."+secretsNote,
		props, []string{"database"}, []string{"postgres"}, nil)
}

// Schema returns the committed form of one binary's schema: indented, with its
// keys in a fixed order, so that a regenerated file differs from the committed
// one only when the schema does.
func Schema(name string) ([]byte, bool) {
	var s m
	switch name {
	case "audit-writer":
		s = writerSchema()
	case "audit-query":
		s = querySchema()
	case "audit-digest":
		s = digestSchema()
	case "audit-verify":
		s = verifySchema()
	case "audit-purge":
		s = purgeSchema()
	case "audit-clock-sync":
		s = clockSyncSchema()
	case "audit-migrate":
		s = migrateSchema()
	default:
		return nil, false
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		panic(err) // the maps above are all encodable
	}
	return buf.Bytes(), true
}
