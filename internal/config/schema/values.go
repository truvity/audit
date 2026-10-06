//nolint:lll // a schema is prose, and a description is one string
package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	policy_ "github.com/truvity/policy"

	"github.com/truvity/audit"
)

// Values returns the schema of the chart's values.
//
// Each component's `config` is the schema its binary validates its file
// against, embedded, so that a configuration which would fail at start-up
// fails at `helm install` and in the chart's own tests: the same file is held
// to the same schema by both. Helm cannot fetch a `$ref`, so the shared shapes
// of truvity/policy are inlined and every reference is made local.
func Values() []byte {
	defs := m{
		"secretEnv": map[string]any{
			"type": "array",
			"description": "Deprecated (version 1 configs only): environment variables from a Secret's keys, the holders of " +
				"the secrets a version-1 config names (`passwordEnv`, `credentialsEnv`, `tokenEnv`). A version-2 config " +
				"names its secrets and reads them from files: see `secretFiles`. A secret is never in `config`.",
			"items": obj("One variable.", m{
				"name":       str("The variable, as the config names it."),
				"secretName": str("The Secret."),
				"key":        str("The key in the Secret."),
				"optional":   boolean("Start without it when the Secret or the key is absent."),
			}, "name", "secretName", "key"),
		},
		"secretFiles": map[string]any{
			"type": "array",
			"description": "Secrets projected as files under /etc/audit/secrets, one per name a version-2 config holds in a " +
				"`...Secret` field (`passwordSecret`, `credentialsSecret`, `tokenSecret`). The config's `secrets` must be " +
				"`{source: file, root: /etc/audit/secrets}`. A secret is never in `config`.",
			"items": obj("One secret.", m{
				"name":       str("The name as the config holds it: the file's path under /etc/audit/secrets, such as `database-password` or `openbao/token`."),
				"secretName": str("The Secret."),
				"key":        str("The key in the Secret."),
				"optional":   boolean("Start without it when the Secret or the key is absent."),
			}, "name", "secretName", "key"),
		},
		"secretMounts": map[string]any{
			"type": "array",
			"description": "Secrets mounted read-only as directories, for a key, a root or a certificate a config " +
				"names by path.",
			"items": obj("One mount.", m{
				"secretName": str("The Secret."),
				"mountPath":  str("The directory it appears in; each key is a file."),
			}, "secretName", "mountPath"),
		},
		"tokens": map[string]any{
			"type": "array",
			"description": "Projected service-account tokens, for a config that names a token file. The kubelet " +
				"replaces each before it expires, and whatever reads it reads it afresh.",
			"items": obj("One token.", m{
				"audience":          str("The audience the token is for."),
				"mountPath":         str("The directory it appears in."),
				"path":              strDefault("The file's name in that directory.", "token"),
				"expirationSeconds": integer("How long it lives.", 600, 3600),
			}, "audience", "mountPath"),
		},
		"serviceAccount": obj("The service account this component runs as.", m{
			"create":      boolean("Create `<fullname>-<component>`. Otherwise the release's own `serviceAccount` is used."),
			"name":        m{"type": "string", "description": "Override the name: the created account's, or, with `create: false`, the existing account to run as. Empty is `<fullname>-<component>` when created and the release's own when not."},
			"annotations": m{"type": "object", "additionalProperties": m{"type": "string"}, "description": "Pod Identity, IRSA or an OpenBAO role binds here."},
		}),
		"image": obj("One of the chart's images.", m{
			"repository": str("Image repository."),
			"tag":        m{"type": "string", "description": "Image tag. Empty is the chart's appVersion."},
		}),
		"selectors": m{"type": "array", "items": m{"type": "object"}},
		"pinned": obj("One image as the release pins it.", m{
			"registry":   m{"type": "string"},
			"repository": m{"type": "string"},
			"tag":        m{"type": "string"},
			"digest":     m{"type": "string"},
		}),
	}

	// What every component takes beyond its configuration.
	platform := func(config string, mounts, tokens bool) m {
		p := m{
			"config":      m{"type": "object", "description": "Rendered as it stands into a ConfigMap and mounted as /etc/audit/config.yaml. Its schema is " + config + "'s: schemas/config/" + config + ".schema.json."},
			"secretFiles": def("secretFiles"),
			"secretEnv":   def("secretEnv"),
		}
		if mounts {
			p["secretMounts"] = def("secretMounts")
		}
		if tokens {
			p["tokens"] = def("tokens")
		}
		return p
	}
	with := func(base, extra m) m {
		for k, v := range extra {
			base[k] = v
		}
		return base
	}
	schedule := str("A cron schedule.")
	job := func(config string, tokens bool, extra m) m {
		props := with(platform(config, true, tokens), m{"enabled": boolean("Run it."), "schedule": schedule})
		if extra != nil {
			props = with(props, extra)
		}
		return obj("A scheduled job: `audit <command> --config`, or `audit-notary --config` for the notary.", props)
	}

	props := m{
		"images": obj("Digest-pinned images, written by packaging the chart and never by hand; one wins over `image`.", m{
			"audit-writer": def("pinned"), "audit-query": def("pinned"), "audit-observe": def("pinned"), "audit-notary": def("pinned"), "audit": def("pinned"),
		}),
		"image": obj("Images.", m{
			"writer": def("image"), "cli": def("image"), "query": def("image"), "observe": def("image"), "notary": def("image"),
			"pullPolicy": m{"enum": []string{"Always", "IfNotPresent", "Never"}, "description": "Pull policy for every image the chart renders."},
		}),
		"imagePullSecrets": m{"type": "array", "items": m{"type": "object"}},
		"nameOverride":     m{"type": "string"},
		"fullnameOverride": m{"type": "string"},
		"mode":             m{"enum": []string{"direct", "stream"}, "description": "`direct`: one process is the front door and the write path. `stream`: a receiver in front and `writer.consumers` writers behind a durable consumer."},
		"replicas":         integer("Pods of the front door: the writer in direct mode, the receiver in stream mode.", 0, nil),
		"writer": obj("The writer: the one pod in direct mode, the consumers in stream mode. `enabled: false` when it runs elsewhere.",
			with(platform("audit-writer", true, true), m{
				"enabled":   boolean("Host the write path in this release. False when the writer runs elsewhere (the writer Lambda behind SQS): no writer, receiver, consumers or Service are rendered, and every recording component needs a `sink` that reaches it (`sqs`, or an external front door's `url`)."),
				"consumers": integer("In stream mode, how many writers consume the stream.", 0, nil),
			})),
		"receiver": obj("Stream mode: the receiver, which serves the sink and publishes to the stream.",
			with(platform("audit-writer", true, true), m{"serviceAccount": def("serviceAccount")})),
		"profiles":                     m{"type": "object", "description": "The profile document a config names as `deployment`: each profile composed from presets.", "additionalProperties": m{"type": "object"}},
		"externalIdentifiersAreOpaque": boolean("The identifiers received for people outside the organisation already mean nothing outside the application's own database."),
		"query": obj("The query service.", with(platform("audit-query", true, true), m{
			"enabled":        boolean("Run it."),
			"replicas":       integer("Pods.", 0, nil),
			"service":        obj("Its Service.", m{"port": integer("The Service's port.", 1, nil)}),
			"grants":         m{"type": "object", "description": "The grants file a config names as `grants`: issuers, presets and rules. See docs/how-to/read-the-trail.md#access."},
			"keysVolume":     boolean("Mount the writer's key directory read-only, for resolve with the local key provider."),
			"serviceAccount": def("serviceAccount"),
			"route": obj("Publish the query service through Gateway API: an HTTPRoute to the Service, and with `securityPolicy` an Envoy Gateway SecurityPolicy on it. Needs `enabled` and, for the route, `parentRefs` and `hostnames`.", m{
				"enabled":        boolean("Render the HTTPRoute."),
				"parentRefs":     m{"type": "array", "description": "The Gateways or ListenerSets the route attaches to, passed through as they are written. Write group, kind, name, namespace and sectionName out in full: the API server defaults what is omitted and a GitOps tool then shows a diff forever.", "items": m{"type": "object"}},
				"hostnames":      m{"type": "array", "description": "The host names the route answers for.", "items": m{"type": "string", "minLength": 1}},
				"pathPrefix":     m{"type": "string", "pattern": "^(/[^/\\s]+)*$", "description": "A path the installation is published under, such as `/myapp`. The route rewrites it to `/` before the service sees it. Empty matches `/` with no rewrite."},
				"annotations":    m{"type": "object", "additionalProperties": m{"type": "string"}, "description": "Annotations of the HTTPRoute."},
				"securityPolicy": m{"type": "object", "not": m{"required": []string{"targetRefs"}}, "description": "When set, an Envoy Gateway SecurityPolicy (gateway.envoyproxy.io/v1alpha1) is rendered with this as its spec, plus a `targetRefs` the chart sets to this HTTPRoute. `targetRefs` itself must not be given."},
			}),
		})),
		"observe": obj("The indexer: follows the archive by cursor and writes the index the query service reads.", with(platform("audit-observe", true, true), m{
			"enabled":        boolean("Run it."),
			"replicas":       integer("Pods. Several are safe, each reading the same cursors, and one is enough.", 0, nil),
			"serviceAccount": def("serviceAccount"),
		})),
		"workloadIdentity": obj("Who is calling the writer: the document a config names as `workloads`.", m{
			"issuers": m{"type": "array", "items": obj("A trusted issuer.", m{
				"url": str("The issuer, exactly as its tokens' iss claim reads."), "audience": str("The audience, when it is not the default."),
			}, "url")},
			"audience": str("The audience an issuer takes when it names none."),
			"workloads": m{"type": "array", "items": obj("A service account and the source it speaks for.", m{
				"issuer": str("Its issuer; required once more than one is trusted."), "subject": str("The service account."), "source": str("The source it speaks for."),
			}, "subject", "source")},
		}),
		"extensions": obj("Projections a product switches on; both render nothing yet.", m{
			"billing": obj("Rollups and a monthly statement.", m{"enabled": boolean("Switch it on.")}),
			"quotas":  obj("Usage counting and the decision point.", m{"enabled": boolean("Switch it on.")}),
		}),
		"catalogues": m{"type": "object", "description": "Catalogue documents mounted for the writer, as name to document, for the `catalogues` key of its config.", "additionalProperties": m{"type": "string"}},
		"migrate": obj("The index schema, applied by a hook Job before the writer rolls.",
			with(platform("audit-migrate", false, false), m{"enabled": boolean("Run it.")})),
		"keysVolume": obj("The local key provider's directory, mounted at /var/lib/audit/keys.", m{
			"enabled":               boolean("Mount a volume. Losing it re-keys every tenant."),
			"size":                  str("The claim's size."),
			"storageClass":          m{"type": "string"},
			"accessModes":           m{"type": "array", "items": m{"type": "string"}},
			"existingClaim":         m{"type": "string", "description": "A claim that already exists. Empty creates one."},
			"ephemeralIsAcceptable": boolean("Keep the keys in an emptyDir when there is no volume. Pseudonyms then change on every restart."),
		}),
		"trust": obj("A CA bundle trusted beside the system roots, mounted at /etc/audit/trust.", m{
			"configMap": m{"type": "string", "description": "The ConfigMap. Empty mounts none."}, "key": str("The key in it."),
		}),
		"service": obj("The writer's Service.", m{"type": m{"type": "string"}, "port": integer("Its port.", 1, nil)}),
		"jobs": obj("The scheduled jobs.", m{
			"notary":    job("audit-notary", true, m{"serviceAccount": def("serviceAccount")}),
			"verify":    job("audit-verify", true, m{"serviceAccount": def("serviceAccount")}),
			"purge":     job("audit-purge", false, m{"serviceAccount": def("serviceAccount")}),
			"clockSync": job("audit-clock-sync", true, m{"serviceAccount": def("serviceAccount")}),
		}),
		"serviceAccount": obj("The release's own service account: the writer's identity (the consumers', in stream mode), and what a component with `create: false` falls back to.", m{
			"create": boolean("Create it."), "annotations": m{"type": "object", "additionalProperties": m{"type": "string"}}, "name": m{"type": "string"},
		}),
		"networkPolicy": obj("Who may reach each component.", m{
			"enabled": boolean("Render the policies."), "ingressFrom": def("selectors"), "queryIngressFrom": def("selectors"),
		}),
		"telemetry": obj("The OpenTelemetry SDK environment every pod carries (ADR 0021): where signals go, never the configuration file.", m{
			"otlp": obj("OTLP export. With `endpoint` set every pod gets OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_EXPORTER_OTLP_PROTOCOL and an OTEL_SERVICE_NAME of its own, then each `extraEnv` entry; empty renders nothing.", m{
				"endpoint": m{"type": "string", "pattern": "^(https?://[^/?#\\s]+.*)?$", "description": "The collector or metrics gateway, an http(s) URL. Empty: no export."},
				"protocol": m{"enum": []string{"http/protobuf", "http/json"}, "default": "http/protobuf", "description": "The OTLP protocol. The exporters are OTLP/HTTP; gRPC is not supported."},
				"extraEnv": m{"type": "object", "propertyNames": m{"pattern": "^OTEL_"}, "additionalProperties": m{"type": "string"}, "description": "Other OpenTelemetry SDK variables, `OTEL_*` only. A secret reaches a pod through `secretEnv`, never here. It cannot carry OTEL_EXPORTER_OTLP_ENDPOINT: `endpoint` is where that is set."},
			}),
		}),
		"terminationGracePeriodSeconds": integer("How long a write-path pod has to stop: longer than the writer's 30s shutdown budget plus `preStopSleepSeconds`, which the chart enforces.", 31, 45),
		"preStopSleepSeconds":           integer("How long the front door sleeps before it is stopped, so that it keeps answering while the endpoints drain. 0 turns it off.", 0, 5),
		"podAnnotations":                m{"type": "object"},
		"podSecurityContext":            m{"type": "object"},
		"securityContext":               m{"type": "object"},
		"resources":                     m{"type": "object"},
		"nodeSelector":                  m{"type": "object"},
		"tolerations":                   m{"type": "array"},
		"affinity":                      m{"type": "object"},
	}
	for k, v := range telemetryValues() {
		props[k] = v
	}
	// The purge job reads no token and mounts only what a config names; the
	// others are as the job helper above gave them.
	jobs := props["jobs"].(m)["properties"].(m)
	delete(jobs["purge"].(m)["properties"].(m), "tokens")

	// A component's config is held to its binary's schema where the component
	// is rendered, and is free to be empty where it is not.
	embedded := map[string]string{}
	for _, name := range []string{"audit-writer", "audit-query", "audit-observe", "audit-verify", "audit-purge", "audit-clock-sync", "audit-migrate", "audit-notary"} {
		// A config is held to version 2 when it says so, and otherwise to version 1
		// (an absent apiVersion is version 1), which the binary still reads for
		// one minor: the chart renders the file as it stands.
		embedded[name] = "config-" + name
		flatten(defs, name, "config-"+name+".v2", Schema)
		flatten(defs, name, "config-"+name+".v1", legacy)
		defs[embedded[name]] = m{
			"if": m{
				"properties": m{"apiVersion": m{"const": Group + "/" + name + "/v2"}},
				"required":   []string{"apiVersion"},
			},
			"then": def("config-" + name + ".v2"),
			"else": def("config-" + name + ".v1"),
		}
	}
	when := func(condition, then m) m { return m{"if": condition, "then": then} }
	configOf := func(name string, path ...string) m {
		inner := m{"properties": m{"config": def(embedded[name])}}
		for i := len(path) - 1; i >= 0; i-- {
			inner = m{"properties": m{path[i]: inner}}
		}
		return inner
	}
	enabled := func(path ...string) m {
		c := m{"properties": m{"enabled": m{"const": true}}, "required": []string{"enabled"}}
		for i := len(path) - 1; i >= 0; i-- {
			c = m{"properties": m{path[i]: c}, "required": []string{path[i]}}
		}
		return c
	}
	all := []any{
		// The writer's config is the writer's, unless it runs elsewhere.
		m{
			"if": m{
				"properties": m{"writer": m{"properties": m{"enabled": m{"const": false}}, "required": []string{"enabled"}}},
				"required":   []string{"writer"},
			},
			"else": configOf("audit-writer", "writer"),
		},
		when(m{"properties": m{"mode": m{"const": "stream"}}, "required": []string{"mode"}}, configOf("audit-writer", "receiver")),
		when(enabled("query"), configOf("audit-query", "query")),
		when(enabled("observe"), configOf("audit-observe", "observe")),
		when(enabled("migrate"), configOf("audit-migrate", "migrate")),
		when(enabled("jobs", "notary"), configOf("audit-notary", "jobs", "notary")),
		when(enabled("jobs", "verify"), configOf("audit-verify", "jobs", "verify")),
		when(enabled("jobs", "purge"), configOf("audit-purge", "jobs", "purge")),
		when(enabled("jobs", "clockSync"), configOf("audit-clock-sync", "jobs", "clockSync")),
	}

	root := m{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"title":                "audit",
		"description":          "The audit trail's write path: the writer, the query service, and the jobs that seal, verify and prune what it writes. Each component's `config` is the schema its binary validates its file against, embedded; everything else is the platform's. Names follow docs/reference/configuration.md.",
		"type":                 "object",
		"additionalProperties": false,
		// `x-` keys are free, so that a values file can anchor what it repeats.
		"patternProperties": m{"^x-": m{}},
		"properties":        props,
		"$defs":             defs,
		"allOf":             []any{appOnly(all)},
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(root); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// flatten puts one binary's configuration schema into defs, made to stand in a
// document that is not its own: its shared shapes become defs beside it under
// its name, every local reference is rewritten to match, and the references to
// truvity/policy's shared shapes are replaced by the shapes themselves.
// legacy is version 1 of a configuration's schema, frozen in schemas/config/v1:
// it is read while the chart still takes a config written in it.
func legacy(name string) ([]byte, bool) {
	b, err := audit.ConfigSchemas.ReadFile("schemas/config/v1/" + name + ".schema.json")
	return b, err == nil
}

func flatten(defs m, name, prefix string, schema func(string) ([]byte, bool)) {
	body, _ := schema(name)
	var s m
	if err := json.Unmarshal(body, &s); err != nil {
		panic(err)
	}
	delete(s, "$schema")
	delete(s, "$id")
	own, _ := s["$defs"].(map[string]any)
	delete(s, "$defs")
	for k, v := range own {
		defs[prefix+"."+k] = rewrite(v, prefix)
	}
	defs[prefix] = rewrite(s, prefix)
}

func rewrite(v any, prefix string) any {
	switch t := v.(type) {
	case map[string]any:
		if r, ok := t["$ref"].(string); ok {
			out := m{}
			for k, x := range t {
				if k != "$ref" {
					out[k] = rewrite(x, prefix)
				}
			}
			switch {
			case strings.HasPrefix(r, "#/$defs/"):
				out["$ref"] = "#/$defs/" + prefix + "." + strings.TrimPrefix(r, "#/$defs/")
			case r == policy+"fragments/secrets.json#/$defs/name":
				for k, x := range fragment("fragments/secrets.json")["$defs"].(map[string]any)["name"].(map[string]any) {
					if _, set := out[k]; !set {
						out[k] = x
					}
				}
			case strings.HasPrefix(r, policy):
				for k, x := range fragment(strings.TrimPrefix(r, policy)) {
					if _, set := out[k]; !set {
						out[k] = rewrite(x, prefix)
					}
				}
			default:
				panic("schema: a reference this chart cannot resolve: " + r)
			}
			return out
		}
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = rewrite(x, prefix)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = rewrite(x, prefix)
		}
		return out
	default:
		return v
	}
}

// fragment reads one of truvity/policy's shared shapes, without its identity.
func fragment(path string) map[string]any {
	body, err := policy_.Schemas.ReadFile("schemas/" + path)
	if err != nil {
		panic(fmt.Sprintf("schema: %s: %v", path, err))
	}
	var s map[string]any
	if err := json.Unmarshal(body, &s); err != nil {
		panic(err)
	}
	delete(s, "$schema")
	delete(s, "$id")
	delete(s, "title")
	return s
}
