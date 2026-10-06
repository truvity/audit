// Package chart holds the chart to the rule that makes a configuration file
// worth having: what the chart renders IS what the values say, and what it
// renders is what the binary will accept.
package chart_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/truvity/audit/internal/config"
)

// The values files whose renders are golden: each is a shape a deployment
// takes, and each is rendered here.
var shapes = []string{
	"testdata/values/direct.yaml",
	"testdata/values/stream.yaml",
	"testdata/values/transit.yaml",
	"testdata/values/attested.yaml",
	"testdata/values/e2e.yaml",
	"examples/direct.yaml",
	"examples/stream.yaml",
	"examples/sqs.yaml",
	"examples/external-writer.yaml",
}

// What each ConfigMap is named for: where its config lives in the values, and
// which schema its binary validates it against.
var components = map[string]struct {
	path   []string
	schema string
}{
	"writer":     {[]string{"writer", "config"}, "audit-writer"},
	"receiver":   {[]string{"receiver", "config"}, "audit-writer"},
	"query":      {[]string{"query", "config"}, "audit-query"},
	"observe":    {[]string{"observe", "config"}, "audit-observe"},
	"migrate":    {[]string{"migrate", "config"}, "audit-migrate"},
	"notary":     {[]string{"jobs", "notary", "config"}, "audit-notary"},
	"verify":     {[]string{"jobs", "verify", "config"}, "audit-verify"},
	"purge":      {[]string{"jobs", "purge", "config"}, "audit-purge"},
	"clock-sync": {[]string{"jobs", "clockSync", "config"}, "audit-clock-sync"},
}

func helm(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("helm")
	if err != nil {
		if os.Getenv("AUDIT_REQUIRE_HELM") != "" {
			t.Fatal("helm is required here (AUDIT_REQUIRE_HELM is set) and is not on the PATH")
		}
		t.Skip("helm is not on the PATH")
	}
	return path
}

func render(t *testing.T, values string) []map[string]any {
	t.Helper()
	cmd := exec.Command(helm(t), "template", "audit", ".", "-f", values)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm template -f %s: %v\n%s", values, err, stderr.String())
	}
	var docs []map[string]any
	for _, part := range strings.Split(string(out), "\n---\n") {
		var doc map[string]any
		if err := yaml.Unmarshal([]byte(part), &doc); err != nil {
			t.Fatalf("a rendered document is not YAML: %v\n%s", err, part)
		}
		if doc != nil {
			docs = append(docs, doc)
		}
	}
	return docs
}

func dig(doc any, path ...string) (any, bool) {
	for _, key := range path {
		m, ok := doc.(map[string]any)
		if !ok {
			return nil, false
		}
		if doc, ok = m[key]; !ok {
			return nil, false
		}
	}
	return doc, true
}

// The chart passes `config` through: the ConfigMap it renders for a component
// holds exactly the block the values gave, with nothing added, renamed or
// dropped, and that block is a file its binary accepts.
func TestTheRenderedConfigurationIsTheValuesConfiguration(t *testing.T) {
	found := 0
	for _, shape := range shapes {
		t.Run(filepath.Base(filepath.Dir(shape))+"/"+filepath.Base(shape), func(t *testing.T) {
			raw, err := os.ReadFile(shape)
			if err != nil {
				t.Fatal(err)
			}
			var values map[string]any
			if err := yaml.Unmarshal(raw, &values); err != nil {
				t.Fatal(err)
			}

			rendered := map[string]bool{}
			for _, doc := range render(t, shape) {
				if doc["kind"] != "ConfigMap" {
					continue
				}
				name, _ := dig(doc, "metadata", "name")
				component, ok := dig(doc, "metadata", "labels", "app.kubernetes.io/component")
				data, hasData := dig(doc, "data", "config.yaml")
				if !ok || !hasData || !strings.HasSuffix(name.(string), "-config") {
					continue
				}
				c, known := components[component.(string)]
				if !known {
					t.Errorf("%s: a configuration for a component this test does not know", name)
					continue
				}
				rendered[component.(string)] = true
				found++

				var got any
				if err := yaml.Unmarshal([]byte(data.(string)), &got); err != nil {
					t.Fatalf("%s: not YAML: %v", name, err)
				}
				want, ok := dig(values, c.path...)
				if !ok {
					t.Errorf("%s is rendered and the values have no %s", name, strings.Join(c.path, "."))
					continue
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("%s is not %s of the values:\n got %v\nwant %v", name, strings.Join(c.path, "."), got, want)
				}
				if err := config.ValidateAsWritten(c.schema, got); err != nil {
					t.Errorf("%s is not a file %s accepts: %v", name, c.schema, err)
				}
			}

			// The other way: a configuration the values give for a component
			// the chart renders must reach a ConfigMap. A component that is
			// switched off renders nothing, and that is not a drop.
			for component, c := range components {
				if _, given := dig(values, c.path...); given && !rendered[component] && enabled(values, c.path) {
					t.Errorf("the values configure %s and the chart rendered no ConfigMap for it", component)
				}
			}
		})
	}
	// A sweep that found nothing proved nothing.
	if !t.Failed() && found < len(shapes) {
		t.Errorf("only %d configurations were compared across %d shapes", found, len(shapes))
	}
}

// enabled says whether the values leave the component's own switch on: a job
// or the query service is off when its `enabled` is false.
func enabled(values map[string]any, path []string) bool {
	parent, ok := dig(values, path[:len(path)-1]...)
	if !ok {
		return true
	}
	if on, ok := dig(parent, "enabled"); ok {
		return on == true
	}
	// Components the chart always renders, or renders for a mode.
	switch path[0] {
	case "receiver":
		mode, _ := dig(values, "mode")
		return mode == "stream"
	case "query", "migrate":
		return false
	}
	return true
}

// In stream mode the receiver and the consumers are different Deployments
// with different ServiceAccounts, and each ServiceAccount exists: on AWS the
// ServiceAccount is the cloud identity, and a receiver must not hold the one
// that writes the archive.
func TestTheReceiverAndTheConsumerRunAsDifferentServiceAccounts(t *testing.T) {
	for _, values := range []string{"testdata/values/stream.yaml", "examples/stream.yaml"} {
		docs := render(t, values)
		accounts := map[string]string{}
		created := map[string]bool{}
		for _, doc := range docs {
			kind, _ := doc["kind"].(string)
			name, _ := dig(doc, "metadata", "name")
			switch kind {
			case "Deployment":
				sa, _ := dig(doc, "spec", "template", "spec", "serviceAccountName")
				accounts[name.(string)] = sa.(string)
			case "ServiceAccount":
				created[name.(string)] = true
			}
		}
		var receiver, consumer string
		for name, sa := range accounts {
			if strings.HasSuffix(name, "-consumer") {
				consumer = sa
			} else if !strings.HasSuffix(name, "-query") && !strings.HasSuffix(name, "-observe") {
				receiver = sa
			}
		}
		if receiver == "" || consumer == "" {
			t.Fatalf("%s: wanted a receiver and a consumer Deployment, got %v", values, accounts)
		}
		if receiver == consumer {
			t.Errorf("%s: the receiver and the consumer both run as %q", values, receiver)
		}
		for _, sa := range []string{receiver, consumer} {
			if !created[sa] {
				t.Errorf("%s: ServiceAccount %q is used but not rendered", values, sa)
			}
		}
	}
}

// The indexer holds the index's write credential, so it runs as an identity of
// its own: not the writer's, which writes the archive, and not the query
// service's, which faces callers. Each ServiceAccount exists, and what the
// Deployment's pods are labelled is what selects them.
func TestTheIndexerRunsAsAServiceAccountOfItsOwn(t *testing.T) {
	for _, values := range []string{"testdata/values/stream.yaml", "examples/direct.yaml", "examples/stream.yaml", "examples/sqs.yaml"} {
		accounts := map[string]string{}
		created := map[string]bool{}
		for _, doc := range render(t, values) {
			kind, _ := doc["kind"].(string)
			name, _ := dig(doc, "metadata", "name")
			switch kind {
			case "Deployment":
				sa, _ := dig(doc, "spec", "template", "spec", "serviceAccountName")
				accounts[name.(string)] = sa.(string)
			case "ServiceAccount":
				created[name.(string)] = true
			}
		}
		var observer string
		for name, sa := range accounts {
			if strings.HasSuffix(name, "-observe") {
				observer = sa
			}
		}
		if observer == "" {
			t.Fatalf("%s: no indexer Deployment in %v", values, accounts)
		}
		for name, sa := range accounts {
			if !strings.HasSuffix(name, "-observe") && sa == observer {
				t.Errorf("%s: the indexer runs as %q, which %s runs as too", values, observer, name)
			}
		}
		if !created[observer] {
			t.Errorf("%s: ServiceAccount %q is used but not rendered", values, observer)
		}
	}
}

// With the writer elsewhere the release hosts no write path at all: no writer
// or receiver Deployment, no consumers, no Service for a front door, no
// ConfigMap for either and no ServiceAccount of the writer's. What is left
// reads, and what records sends to the queue.
func TestAnExternalWriterRendersNoWritePath(t *testing.T) {
	const values = "examples/external-writer.yaml"
	for _, doc := range render(t, values) {
		kind, _ := doc["kind"].(string)
		name, _ := dig(doc, "metadata", "name")
		n, _ := name.(string)
		switch {
		case kind == "Deployment" && !strings.HasSuffix(n, "-query") && !strings.HasSuffix(n, "-observe"):
			t.Errorf("a write path Deployment, %s", n)
		case kind == "Service" && n != "audit-query":
			t.Errorf("a Service that is not the query service's, %s", n)
		case kind == "ConfigMap" && (strings.HasSuffix(n, "-writer-config") || strings.HasSuffix(n, "-receiver-config")):
			t.Errorf("a configuration for the write path, %s", n)
		case kind == "ServiceAccount" && n == "audit":
			t.Errorf("the writer's ServiceAccount, %s", n)
		}
	}
}

// overlay renders values with a second file laid over them.
func overlay(t *testing.T, values, extra string) []map[string]any {
	t.Helper()
	p := filepath.Join(t.TempDir(), "overlay.yaml")
	if err := os.WriteFile(p, []byte(extra), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(helm(t), "template", "audit", ".", "-f", values, "-f", p)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, stderr.String())
	}
	var docs []map[string]any
	for _, part := range strings.Split(string(out), "\n---\n") {
		var doc map[string]any
		if err := yaml.Unmarshal([]byte(part), &doc); err != nil {
			t.Fatal(err)
		}
		if doc != nil {
			docs = append(docs, doc)
		}
	}
	return docs
}

// podOf is the pod template of the Deployment with this name.
func podOf(t *testing.T, docs []map[string]any, name string) map[string]any {
	t.Helper()
	for _, doc := range docs {
		if doc["kind"] != "Deployment" {
			continue
		}
		if n, _ := dig(doc, "metadata", "name"); n == name {
			pod, ok := dig(doc, "spec", "template")
			if !ok {
				t.Fatalf("%s has no pod template", name)
			}
			return pod.(map[string]any)
		}
	}
	t.Fatalf("no Deployment %s", name)
	return nil
}

func annotation(t *testing.T, pod map[string]any, key string) (string, bool) {
	t.Helper()
	v, ok := dig(pod, "metadata", "annotations", key)
	s, _ := v.(string)
	return s, ok
}

// A writer pod has longer to stop than the writer takes to flush, and the front
// door, but not the consumer that nothing calls, sleeps first so that it keeps
// answering while its endpoints drain.
func TestAWriterPodOutlastsItsShutdownAndTheFrontDoorSleepsFirst(t *testing.T) {
	docs := render(t, "testdata/values/stream.yaml")
	for _, name := range []string{"audit", "audit-consumer"} {
		pod := podOf(t, docs, name)
		grace, _ := dig(pod, "spec", "terminationGracePeriodSeconds")
		if g, _ := grace.(float64); g <= 30+5 {
			t.Errorf("%s: terminationGracePeriodSeconds = %v, must outlast the 30s shutdown and the preStop sleep", name, grace)
		}
		containers, _ := dig(pod, "spec", "containers")
		pre, has := dig(containers.([]any)[0], "lifecycle", "preStop", "sleep", "seconds")
		if front := name == "audit"; front != has {
			t.Errorf("%s: preStop sleep present = %v, want %v (%v)", name, has, front, pre)
		}
	}
}

func TestTheDeploymentChecksumCoversTheWholeProfileDocument(t *testing.T) {
	was := podOf(t, render(t, "testdata/values/direct.yaml"), "audit")
	flipped := podOf(t, overlay(t, "testdata/values/direct.yaml", "externalIdentifiersAreOpaque: false\n"), "audit")
	a, _ := annotation(t, was, "checksum/deployment")
	b, _ := annotation(t, flipped, "checksum/deployment")
	if a == "" || a == b {
		t.Errorf("flipping externalIdentifiersAreOpaque left checksum/deployment as it was (%q, %q): a pod would keep the old profile document", a, b)
	}
}

func TestTheCataloguesChecksumIsOnTheWritersThatReadThem(t *testing.T) {
	const catalogues = "catalogues:\n  shop.yaml: |\n    source: shop\n    version: \"1.0.0\"\n"
	docs := overlay(t, "testdata/values/stream.yaml", catalogues)
	if _, ok := annotation(t, podOf(t, docs, "audit-consumer"), "checksum/catalogues"); !ok {
		t.Error("the writer that registers catalogues does not restart when one changes")
	}
	if _, ok := annotation(t, podOf(t, docs, "audit"), "checksum/catalogues"); ok {
		t.Error("the receiver holds no catalogues and should not restart for them")
	}
	changed := overlay(t, "testdata/values/stream.yaml", strings.Replace(catalogues, "1.0.0", "1.0.1", 1))
	a, _ := annotation(t, podOf(t, docs, "audit-consumer"), "checksum/catalogues")
	b, _ := annotation(t, podOf(t, changed, "audit-consumer"), "checksum/catalogues")
	if a == b {
		t.Error("a changed catalogue left the checksum as it was")
	}
}

// A secret projected as a file is readable by the pod's own user and group and by
// nobody else.
func TestSecretFilesAreProjectedWithMode0440(t *testing.T) {
	docs := render(t, "examples/direct.yaml")
	var found int
	for _, d := range docs {
		spec, _ := dig(d, "spec", "template", "spec")
		if spec == nil {
			continue
		}
		vols, _ := dig(spec, "volumes")
		for _, v := range vols.([]any) {
			if n, _ := dig(v, "name"); n != "secret-files" {
				continue
			}
			found++
			if mode, _ := dig(v, "projected", "defaultMode"); mode != float64(0o440) {
				t.Errorf("secret-files defaultMode = %v, want %d", mode, 0o440)
			}
		}
	}
	if found == 0 {
		t.Fatal("no secret-files volume was rendered by examples/direct.yaml")
	}
}

const routeValues = `
query:
  route:
    enabled: true
    parentRefs:
      - group: gateway.networking.k8s.io
        kind: Gateway
        name: public
        namespace: gateway
        sectionName: https
    hostnames: [audit.example.com]
`

func kindOf(docs []map[string]any, kind string) map[string]any {
	for _, d := range docs {
		if d["kind"] == kind {
			return d
		}
	}
	return nil
}

// The query service is published only when asked: under a path prefix the
// route strips it, and the backend's weight is written out so that a GitOps
// tool has nothing to diff against the defaulted object.
func TestTheQueryRouteStripsItsPrefixAndWritesTheWeight(t *testing.T) {
	const stream = "testdata/values/stream.yaml"
	if kindOf(render(t, stream), "HTTPRoute") != nil {
		t.Fatal("an HTTPRoute is rendered by default")
	}

	docs := overlay(t, stream, routeValues+"    pathPrefix: /myapp\n    securityPolicy:\n      jwt:\n        providers: []\n")
	route := kindOf(docs, "HTTPRoute")
	if route == nil {
		t.Fatal("no HTTPRoute")
	}
	rule, _ := dig(route, "spec", "rules")
	r := rule.([]any)[0].(map[string]any)
	if v, _ := dig(r, "matches"); v.([]any)[0].(map[string]any)["path"].(map[string]any)["value"] != "/myapp/audit.v1.QueryService" {
		t.Errorf("match = %v", v)
	}
	if v, ok := dig(r, "filters"); !ok || v.([]any)[0].(map[string]any)["urlRewrite"].(map[string]any)["path"].(map[string]any)["replacePrefixMatch"] != "/audit.v1.QueryService" {
		t.Errorf("filters = %v", v)
	}
	if w, _ := dig(r["backendRefs"].([]any)[0], "weight"); w != float64(1) {
		t.Errorf("weight = %v, want 1 written out", w)
	}
	sp := kindOf(docs, "SecurityPolicy")
	if sp == nil {
		t.Fatal("no SecurityPolicy")
	}
	if n, _ := dig(sp, "spec", "targetRefs"); n.([]any)[0].(map[string]any)["name"] != route["metadata"].(map[string]any)["name"] {
		t.Errorf("targetRefs = %v", n)
	}

	docs = overlay(t, stream, routeValues)
	if v, _ := dig(kindOf(docs, "HTTPRoute"), "spec", "rules"); v.([]any)[0].(map[string]any)["matches"].([]any)[0].(map[string]any)["path"].(map[string]any)["value"] != "/audit.v1.QueryService" {
		t.Errorf("an unprefixed route must match the service path only, not the health endpoints: %v", v)
	}
	r = kindOf(docs, "HTTPRoute")["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)
	if _, has := r["filters"]; has {
		t.Error("a route without a prefix rewrites")
	}
	if kindOf(docs, "SecurityPolicy") != nil {
		t.Error("a SecurityPolicy without being asked")
	}
}
