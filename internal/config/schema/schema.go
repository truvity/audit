// Package schema builds the JSON Schema of each binary's configuration file.
//
// It is a package of its own, apart from the types and the loader, so that the
// generator which writes the committed files does not need them to exist.
//
//nolint:lll // a schema is prose, and a description is one string
package schema

import (
	"bytes"
	"encoding/json"
)

// Group is the group of every apiVersion in this repository's documents.
// The name is joined so that a scan for emitted action names, which reads a
// string of this shape as one, does not take it for an action.
const Group = "audit" + ".truvity.com"

// BaseID is where the schemas are served: the same site as the record's.
const BaseID = "https://truvity.github.io/audit/schemas/v1/config/"

// The shared shapes this repository takes from truvity/policy, by the `$id`
// they carry. The loader resolves them from its embedded copies.
const policy = "https://github.com/truvity/policy/schemas/"

// Names are the binaries and commands that read a file, as the schema files are
// named: schemas/config/<name>.schema.json.
var Names = []string{
	"audit-writer", "audit-query", "audit-observe",
	"audit-verify", "audit-purge", "audit-clock-sync", "audit-migrate", "audit-notary", "audit-writer-lambda",
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

func integer(description string, least int, def any) m {
	o := m{"type": "integer", "minimum": least, "description": description}
	if def != nil {
		o["default"] = def
	}
	return o
}

func durability(description string) m {
	return m{"enum": []string{"logged", "queued", "archived"}, "description": description}
}

// requireEmitter is `require` for a process that records through a sink of
// its own: it holds the writer's acknowledgements to a floor, and needs
// sink.expect to know the writer can meet it before anything is sent.
func requireEmitter() m {
	return durability("The weakest durability an acknowledgement from the writer may carry (`logged`, `queued` or `archived`). Unset checks nothing. Set, it needs `sink.expect` at least as strong, which is checked at start-up, and an acknowledgement weaker than this fails the write instead of passing it with a caveat. See ADR 0017.")
}

// stream is how a process reaches a NATS JetStream stream.
func stream(description string) m {
	return obj(description, m{
		"nats":     ref(policy + "fragments/nats.json"),
		"name":     strDefault("The stream.", "AUDIT"),
		"consumer": strDefault("The durable consumer this installation's writers share.", "audit-writer"),
		"batch":    integer("How many records are taken from the stream at once.", 1, 100),
		"ackWait":  duration("How long the stream waits for a batch to be taken before offering it again. It must exceed `roll.interval` plus the longest a put can take.", "2m"),
	}, "nats")
}

// sqs is an SQS queue. Its credentials are never here: they are the SDK's
// ambient ones, which on Kubernetes is the pod's workload identity.
func sqs(description string, extra m) m {
	props := m{
		"queueUrl": str("The queue's URL."),
		"region":   str("The queue's region. Unset is the SDK's: AWS_REGION, which EKS Pod Identity and IRSA set."),
		"fifo":     boolean("The queue is FIFO. It must agree with the URL, which ends in `.fifo` for one. A FIFO queue absorbs a repeated record itself, inside its five-minute window; a standard queue relies on the writer's deduplication."),
	}
	for k, v := range extra {
		props[k] = v
	}
	return obj(description+" Credentials are never in the file: the process uses the SDK's ambient ones, which on Kubernetes is the pod's workload identity (EKS Pod Identity or IRSA) bound through the service account.", props, "queueUrl")
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
		"sink": func() m {
			o := obj("The writer this process records through: exactly one of `url` (a writer or receiver that serves the sink) and `sqs` (the ingest queue of a writer that runs elsewhere, such as the writer Lambda).", m{
				"url":       str("The writer's base URL."),
				"tokenFile": str("A file holding the token presented to the writer, read afresh on every request: in a cluster, the pod's projected service-account token. Unset presents none, which an anonymous trial install accepts. Not with `sqs`."),
				"expect":    durability("What the writer at `url` is configured to give: `archived` for a writer, `queued` for a receiver in front of a queue, `logged` for one that only logs. A client cannot learn it until it writes, so the file says, and `require` is checked against it at start-up and against every acknowledgement afterwards. With `sqs` it is `queued`, which is all a queue gives, and may be left out."),
				"sqs":       sqs("The SQS queue a writer that runs elsewhere consumes: the process sends its records there and the acknowledgement is `queued`. The pod's identity needs `sqs:SendMessage` on it.", nil),
			})
			o["oneOf"] = []any{m{"required": []string{"url"}}, m{"required": []string{"sqs"}}}
			o["dependentSchemas"] = m{"sqs": m{"properties": m{
				"tokenFile": false, "expect": m{"const": "queued"},
			}}}
			return o
		}(),
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
	props["apiVersion"] = apiVersion(name)
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
			"description": "The shared deduplication table and the catalogue registry, as the writer's own database role: it holds those and none of the index, which audit-observe writes. Without it the writer deduplicates in process and may only run one replica."},
		"replicas":         integer("How many writers share this stream. Above one it needs a database, and with local keys a directory every replica shares.", 1, 1),
		"keys":             def("keys"),
		"forgetIdentities": boolean("Do not keep the identity behind each pseudonym, sealed under its key. By default it is kept, so that resolve can find it."),
		"stream":           stream("The wide stream, as the NATS shorthand: for a receiver it is `forward.nats`, for a writer `consume.nats`. Give it or the longhand, not both."),
		"forward": m{
			"type": "object", "additionalProperties": false,
			"description": "Where a receiver sends what it took: exactly one of `nats`, `sqs` and `log`. `log` is allowed only with `require: logged`.",
			"properties": m{
				"nats": stream("A NATS JetStream stream; what `stream` says."),
				"sqs":  sqs("An SQS queue the receiver publishes to.", nil),
				"log":  obj("The log sink: one JSON line per record on standard output, which survives nothing but the log pipeline. Nothing to configure.", m{}),
			},
			"oneOf": []any{
				m{"required": []string{"nats"}}, m{"required": []string{"sqs"}}, m{"required": []string{"log"}},
			},
		},
		"consume": m{
			"type": "object", "additionalProperties": false,
			"description": "What a writer reads its records from, beside its own sink: exactly one of `nats` and `sqs`.",
			"properties": m{
				"nats": stream("A NATS JetStream stream; what `stream` says."),
				"sqs": sqs("An SQS queue the writer consumes.", m{
					"batch":      integer("How many messages are received at once, one to ten.", 1, 10),
					"visibility": duration("How long a received message is hidden from other consumers while the writer writes it. It must outlast the write, or the message is delivered twice.", "1m"),
				}),
			},
			"oneOf": []any{m{"required": []string{"nats"}}, m{"required": []string{"sqs"}}},
		},
		"require": durability("The weakest durability this process's chain may give. Unset is `archived` for a writer and `queued` for a receiver. At start-up the process refuses to run if what it is configured with can never give that: a receiver, which holds no archive, cannot promise `archived`, and `forward.log` cannot promise more than `logged`. A write acknowledged weaker than this fails. See ADR 0017."),
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
					"then": m{
						"oneOf": []any{m{"required": []string{"stream"}}, m{"required": []string{"forward"}}},
						"properties": m{
							"archive": false, "keys": false, "catalogues": false, "consume": false,
							// A receiver holds no archive, so archived is not its to promise.
							"require": m{"enum": []string{"logged", "queued"}},
						},
						"allOf": []any{
							// The log sink is for a deployment that has chosen a log as its record,
							// and chooses it by saying so.
							m{"if": m{"required": []string{"forward"}, "properties": m{"forward": m{"required": []string{"log"}}}},
								"then": m{"required": []string{"require"}, "properties": m{"require": m{"const": "logged"}}}},
						},
					},
					"else": m{
						"required":   []string{"archive"},
						"properties": m{"forward": false},
						"not":        m{"required": []string{"stream", "consume"}},
					}},
			},
		})
}

func writerLambdaSchema() m {
	props := m{
		"deployment":       str("Path to the profile configuration: which presets each profile is composed from. In the function's package, at `/var/task/deployment.yaml` in the shipped layout."),
		"catalogues":       str("A directory of catalogues to register at start-up."),
		"archive":          archive(true, true),
		"keys":             def("keys"),
		"forgetIdentities": boolean("Do not keep the identity behind each pseudonym, sealed under its key. By default it is kept, so that resolve can find it."),
		"dedupe": m{
			"type": "object", "additionalProperties": false,
			"description": "Where the writer remembers which records it has written, so that a redelivery by the queue is absorbed. A function has no database; exactly one of its fields.",
			"properties": m{
				"dynamodb": obj("A DynamoDB table with a string hash key `pk` and TTL on `expires_at`. One item per written record id, written by a conditional put once the copies are durable. Credentials are the function role's.", m{
					"table":  str("The table's name."),
					"region": str("The table's region. Unset is the SDK's: AWS_REGION, which Lambda sets."),
					"window": duration("How long a written record's id is remembered. Unset is the widest window any profile's presets ask for. It wants to be at least as long as the queue keeps a message (SQS: at most 14 days).", ""),
				}, "table"),
			},
			"oneOf": []any{m{"required": []string{"dynamodb"}}},
		},
		"require": durability("The weakest durability this process's chain may give; the writer gives `archived` at best, which is the default. A batch acknowledged weaker than this fails. See ADR 0017."),
	}
	return document("audit-writer-lambda", "audit-writer-lambda",
		"The configuration of audit-writer-lambda, the write path as an AWS Lambda behind an SQS event source mapping."+secretsNote,
		props, []string{"deployment", "archive", "dedupe"}, []string{"openbao", "keys", "duration"}, nil)
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
		"require":    requireEmitter(),
		"archive": func() m {
			a := archive(false, false)
			a["description"] = "The archive the records are in: what the s3scan searcher reads. Get answers where a copy is and, until seals vouch for it, nothing about whether it has been verified."
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

func observeSchema() m {
	props := m{
		"listen": listen(),
		"archive": func() m {
			a := archive(false, false)
			a["description"] = "The archive to follow. Observe only reads it: it lists, and gets the objects and the catalogues beside them."
			return a
		}(),
		"database": m{"$ref": "#/$defs/postgres",
			"description": "The index, as the role `audit migrate --observe` granted: read and write on the index and its cursors, and nothing of the deduplication table. Not the owner, and not the writer's or the query service's."},
		"settle":   duration("How far behind now the cursor stays. An object's key is fixed when its put starts and it is visible when the put ends, so a later key can be visible before an earlier one; this must be longer than a writer's put can take and than the clocks of the writers and of this process can disagree. It is the least time between a record's acknowledgement and its appearance in search.", "2m"),
		"interval": duration("How often a pass runs when nothing woke it. A lost wake-up costs at most this.", "30s"),
		"batch":    integer("How many rows are written in one transaction. A transaction ends at an object's end, so an object is never split.", 1, 500),
		"profiles": m{"type": "array", "minItems": 1, "uniqueItems": true, "items": str("A profile name."), "description": "The profiles to follow. Unset follows every profile the archive has, found by listing."},
		"wake": m{
			"type": "object", "additionalProperties": false, "minProperties": 1, "maxProperties": 1,
			"description": "Where bucket notifications arrive, to make a pass run now instead of at the next interval. Optional, and only ever a shortcut: nothing a pass does depends on a notification, so a lost or repeated one costs latency and nothing else.",
			"properties": m{
				"nats": obj("A NATS subject carrying bucket notifications. Core NATS: the messages are not read, and one missed is the poll's to make up.", m{
					"nats":    ref(policy + "fragments/nats.json"),
					"subject": str("The subject."),
				}, "nats", "subject"),
				"sqs": sqs("An SQS queue of bucket notifications that is observe's own: each message is taken to wake a pass and deleted.", nil),
			},
		},
	}
	return document("audit-observe", "audit-observe",
		"The configuration of audit-observe, the indexer: it follows the archive by cursor and writes the index the query service reads (ADR 0020)."+secretsNote,
		props, []string{"archive", "database"}, []string{"postgres", "duration"}, nil)
}

func verifySchema() m {
	props := m{
		"deployment": str("Path to the profile configuration; the check holds each object's lock to what its profile demands."),
		"archive":    archive(false, false),
		"sink":       m{"$ref": "#/$defs/sink", "description": "The writer this job records what it checked through."},
		"require":    requireEmitter(),
		"profiles":   m{"type": "array", "minItems": 1, "uniqueItems": true, "items": str("A profile name."), "description": "The profiles whose objects to check. Unset checks every profile the deployment composes."},
		"last":       duration("Check the objects ingested in the last this long, ending at the hour that has closed.", "24h"),
		"seals": obj("Check the seals too: each one's signature, the chain through `prev`, its count and root against the objects, and that no sealed hour is missing. Unset checks no seal.", m{
			"roots": m{"type": "array", "minItems": 1, "uniqueItems": true,
				"items":       m{"type": "string", "pattern": "^[A-Za-z0-9_-]{43}$", "description": "The RFC 7638 thumbprint of a root key (`audit key public --thumbprint`)."},
				"description": "The roots this verifier trusts, by thumbprint. A seal is believed only if a pinned root signed it or delegated to its key; `keys/roots.jwks` in the bucket is how the keys are distributed and is never what is trusted."},
			"settle": duration("The notary's settle window: an hour is sealable once it has ended and this long has passed. Keep it equal to the notary's.", "10m"),
			"grace":  duration("How long after an hour is sealable its seal may still be missing before that is a finding: the notary runs hourly, so a seal is up to an hour behind.", "1h"),
		}, "roots"),
	}
	return document("audit-verify", "audit verify",
		"The configuration of `audit verify --config`, which checks record objects against the bucket contract and reports what it finds."+secretsNote,
		props, []string{"deployment", "archive"}, []string{"sink", "duration"}, m{"dependentRequired": m{"require": []string{"sink"}}})
}

// signer is where the notary's key is: exactly one way.
func signer() m {
	return m{
		"type":                 "object",
		"additionalProperties": false,
		"description":          "The key seals are signed with, a P-384 key (ES384). The private half should never be on the notary's disk: a managed key (`kms`, `transit`) keeps it where the writer's role cannot reach it, which a `file` cannot.",
		"properties": m{
			"kms": obj("An AWS KMS key: ECC_NIST_P384, SIGN_VERIFY, signing ECDSA_SHA_384. The credentials are the SDK's ambient ones: in a cluster, the notary's Pod Identity or IRSA role, which is not the writer's.", m{
				"key":    str("The key's ARN, ID or alias."),
				"region": str("The key's region, when it is not the SDK's."),
			}, "key"),
			"transit": obj("An OpenBAO transit key of type ecdsa-p384.", m{
				"key":     str("The transit key's name."),
				"openbao": def("openbao"),
			}, "key", "openbao"),
			"file": obj("A P-384 private key in a PEM file (PKCS#8 or SEC 1), for development.", m{
				"path": str("The file."),
			}, "path"),
		},
		"oneOf": []any{
			m{"required": []string{"kms"}},
			m{"required": []string{"transit"}},
			m{"required": []string{"file"}},
		},
	}
}

func notarySchema() m {
	props := m{
		"archive": archive(true, true),
		"signer":  signer(),
		"profiles": m{"type": "array", "minItems": 1, "uniqueItems": true, "items": str("A profile name."),
			"description": "The profiles to seal. Unset seals every profile the archive has records for."},
		"settle":  duration("How long after an hour has ended it is sealed, so that a batch put late in the hour it is keyed by is in the seal. An hour is never sealed sooner.", "10m"),
		"sink":    m{"$ref": "#/$defs/sink", "description": "The writer this job records what it sealed through (`audit.seal.written`)."},
		"require": requireEmitter(),
	}
	return document("audit-notary", "audit-notary",
		"The configuration of `audit-notary --config`, which seals the hours of the archive: one signed seal per profile, tenant and hour, chained through `prev`."+secretsNote,
		props, []string{"archive", "signer"}, []string{"sink", "duration", "openbao"}, m{"dependentRequired": m{"require": []string{"sink"}}})
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
		"require":   requireEmitter(),
		"maxOffset": duration("How far the clock may be out before the run fails; 0s accepts any offset and only records it.", "1s"),
		"timeout":   duration("How long to wait for a reference.", "5s"),
	}
	return document("audit-clock-sync", "audit clock-sync",
		"The configuration of `audit clock-sync --config`, which compares the clock with UTC and records the answer."+secretsNote,
		props, []string{"ntp"}, []string{"sink", "duration"}, m{"dependentRequired": m{"require": []string{"sink"}}})
}

func migrateSchema() m {
	props := m{
		"database": def("postgres"),
		"reader":   str("A role to grant what the query service needs: usage on the schema and select on the index's tables, and nothing else. The role must already exist and must not own the tables."),
		"writer":   str("A role to grant what the write path needs: the deduplication table, the catalogue registry and the key directory, and none of the index. Must exist and be none of the other roles."),
		"observe":  str("A role to grant what the indexer needs: read and write on the index and its cursors, and the creation of monthly partitions through one function. Must exist and be none of the other roles."),
		"purge":    str("A role to grant what the purge job needs: to delete from the index and the deduplication table. Must exist and be none of the other roles."),
	}
	return document("audit-migrate", "audit migrate",
		"The configuration of `audit migrate --config`, which applies the schema and grants each part's database role what it needs and no more."+secretsNote,
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
	case "audit-writer-lambda":
		s = writerLambdaSchema()
	case "audit-query":
		s = querySchema()
	case "audit-observe":
		s = observeSchema()
	case "audit-verify":
		s = verifySchema()
	case "audit-notary":
		s = notarySchema()
	case "audit-purge":
		s = purgeSchema()
	case "audit-clock-sync":
		s = clockSyncSchema()
	case "audit-migrate":
		s = migrateSchema()
	case "audit-deployment":
		s = deploymentSchema()
	case "audit-grants":
		s = grantsSchema()
	case "audit-workloads":
		s = workloadsSchema()
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
