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
	"migrate":    {[]string{"migrate", "config"}, "audit-migrate"},
	"digest":     {[]string{"jobs", "digest", "config"}, "audit-digest"},
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
				if err := config.Validate(c.schema, got); err != nil {
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
			} else if !strings.HasSuffix(name, "-query") {
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
