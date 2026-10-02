//nolint:lll // a schema is prose, and a description is one string
package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	policy_ "github.com/truvity/policy"
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
			"description": "Environment variables from a Secret's keys: the holders of the secrets a config names " +
				"(`passwordEnv`, `credentialsEnv`, `tokenEnv`). A secret is never in `config`.",
			"items": obj("One variable.", m{
				"name":       str("The variable, as the config names it."),
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
		"serviceAccount": obj("A service account the chart creates for this component.", m{
			"create":      boolean("Create it. Otherwise the release's own is used."),
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
			"config":    m{"type": "object", "description": "Rendered as it stands into a ConfigMap and mounted as /etc/audit/config.yaml. Its schema is " + config + "'s: schemas/config/" + config + ".schema.json."},
			"secretEnv": def("secretEnv"),
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
		return obj("A scheduled job: `audit <command> --config`.", props)
	}

	props := m{
		"images": obj("Digest-pinned images, written by packaging the chart and never by hand; one wins over `image`.", m{
			"audit-writer": def("pinned"), "audit-query": def("pinned"), "audit": def("pinned"),
		}),
		"image": obj("Images.", m{
			"writer": def("image"), "cli": def("image"), "query": def("image"),
			"pullPolicy": m{"enum": []string{"Always", "IfNotPresent", "Never"}, "description": "Pull policy for every image the chart renders."},
		}),
		"imagePullSecrets": m{"type": "array", "items": m{"type": "object"}},
		"nameOverride":     m{"type": "string"},
		"fullnameOverride": m{"type": "string"},
		"mode":             m{"enum": []string{"direct", "stream"}, "description": "`direct`: one process is the front door and the write path. `stream`: a receiver in front and `writer.consumers` writers behind a durable consumer."},
		"replicas":         integer("Pods of the front door: the writer in direct mode, the receiver in stream mode.", 0, nil),
		"writer": obj("The writer: the one pod in direct mode, the consumers in stream mode.",
			with(platform("audit-writer", true, true), m{"consumers": integer("In stream mode, how many writers consume the stream.", 0, nil)})),
		"receiver": obj("Stream mode: the receiver, which serves the sink and publishes to the stream.",
			platform("audit-writer", true, true)),
		"profiles":                     m{"type": "object", "description": "The profile document a config names as `deployment`: each profile composed from presets.", "additionalProperties": m{"type": "object"}},
		"externalIdentifiersAreOpaque": boolean("The identifiers received for people outside the organisation already mean nothing outside the application's own database."),
		"query": obj("The query service.", with(platform("audit-query", true, true), m{
			"enabled":        boolean("Run it."),
			"replicas":       integer("Pods.", 0, nil),
			"service":        obj("Its Service.", m{"port": integer("The Service's port.", 1, nil)}),
			"grants":         m{"type": "object", "description": "The grants file a config names as `grants`: issuers, presets and rules. See docs/guides/read.md#access."},
			"keysVolume":     boolean("Mount the writer's key directory read-only, for resolve with the local key provider."),
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
			"enabled":       boolean("Mount a volume. Losing it re-keys every tenant."),
			"size":          str("The claim's size."),
			"storageClass":  m{"type": "string"},
			"accessModes":   m{"type": "array", "items": m{"type": "string"}},
			"existingClaim": m{"type": "string", "description": "A claim that already exists. Empty creates one."},
		}),
		"trust": obj("A CA bundle trusted beside the system roots, mounted at /etc/audit/trust.", m{
			"configMap": m{"type": "string", "description": "The ConfigMap. Empty mounts none."}, "key": str("The key in it."),
		}),
		"service": obj("The writer's Service.", m{"type": m{"type": "string"}, "port": integer("Its port.", 1, nil)}),
		"jobs": obj("The scheduled jobs.", m{
			"digest":    job("audit-digest", true, m{"serviceAccount": def("serviceAccount")}),
			"verify":    job("audit-verify", true, m{"serviceAccount": def("serviceAccount")}),
			"purge":     job("audit-purge", false, nil),
			"clockSync": job("audit-clock-sync", true, nil),
		}),
		"serviceAccount": obj("The release's own service account.", m{
			"create": boolean("Create it."), "annotations": m{"type": "object", "additionalProperties": m{"type": "string"}}, "name": m{"type": "string"},
		}),
		"networkPolicy": obj("Who may reach each component.", m{
			"enabled": boolean("Render the policies."), "ingressFrom": def("selectors"), "queryIngressFrom": def("selectors"),
		}),
		"podAnnotations":     m{"type": "object"},
		"podSecurityContext": m{"type": "object"},
		"securityContext":    m{"type": "object"},
		"resources":          m{"type": "object"},
		"nodeSelector":       m{"type": "object"},
		"tolerations":        m{"type": "array"},
		"affinity":           m{"type": "object"},
	}
	// The purge job reads no token and mounts only what a config names; the
	// others are as the job helper above gave them.
	jobs := props["jobs"].(m)["properties"].(m)
	delete(jobs["purge"].(m)["properties"].(m), "tokens")

	// A component's config is held to its binary's schema where the component
	// is rendered, and is free to be empty where it is not.
	embedded := map[string]string{}
	for _, name := range []string{"audit-writer", "audit-query", "audit-digest", "audit-verify", "audit-purge", "audit-clock-sync", "audit-migrate"} {
		embedded[name] = "config-" + name
		flatten(defs, name)
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
		// The writer is always rendered, and its config is always the writer's.
		configOf("audit-writer", "writer"),
		when(m{"properties": m{"mode": m{"const": "stream"}}, "required": []string{"mode"}}, configOf("audit-writer", "receiver")),
		when(enabled("query"), configOf("audit-query", "query")),
		when(enabled("migrate"), configOf("audit-migrate", "migrate")),
		when(enabled("jobs", "digest"), configOf("audit-digest", "jobs", "digest")),
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
		"allOf":             all,
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
func flatten(defs m, name string) {
	body, _ := Schema(name)
	var s m
	if err := json.Unmarshal(body, &s); err != nil {
		panic(err)
	}
	delete(s, "$schema")
	delete(s, "$id")
	prefix := "config-" + name
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
			case strings.HasPrefix(r, policy):
				for k, x := range fragment(strings.TrimPrefix(r, policy)) {
					if _, set := out[k]; !set {
						out[k] = x
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
