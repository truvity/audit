// Package metaschema validates a document against one of the meta-schemas this
// repository publishes: the catalogue format, the preset format, and the
// constraints every extension-slot schema must satisfy.
package metaschema

import (
	"fmt"
	"path"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"sigs.k8s.io/yaml"

	"github.com/truvity/audit"
)

var (
	mu       sync.Mutex
	compiled = map[string]*jsonschema.Schema{}
)

// Validate checks a YAML or JSON document against the named meta-schema.
func Validate(name string, document []byte) error {
	schema, err := load(name)
	if err != nil {
		return err
	}
	asJSON, err := yaml.YAMLToJSON(document)
	if err != nil {
		return fmt.Errorf("not valid YAML or JSON: %w", err)
	}
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(asJSON)))
	if err != nil {
		return err
	}
	if err := schema.Validate(doc); err != nil {
		return fmt.Errorf("does not satisfy %s: %w", name, err)
	}
	return nil
}

func load(name string) (*jsonschema.Schema, error) {
	mu.Lock()
	defer mu.Unlock()
	if s, ok := compiled[name]; ok {
		return s, nil
	}
	raw, err := audit.Schemas.ReadFile(path.Join("schemas", name))
	if err != nil {
		return nil, err
	}
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(name, doc); err != nil {
		return nil, err
	}
	s, err := c.Compile(name)
	if err != nil {
		return nil, err
	}
	compiled[name] = s
	return s, nil
}
