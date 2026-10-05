package auditpulumi

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// An application's catalogue is a document and the data schemas it references:
// an action's `data_schema`, a kind's `attributes_schema`, a meter's
// `dimensions_schema`, a context area. The writer reads them the way
// sdk/catalogue.LoadFS does, the document and every `.json` beside it in the
// same directory, and refuses to start when a referenced schema is not there or
// a schema there is referenced by nothing. So a catalogue that carries schemas
// gets a directory of its own in the layer, `catalogues/<stem>/`, holding the
// document and exactly its schemas; one without stays `catalogues/<file>`, where
// it always was, and no schema is ever beside it.

// isCatalogueFile is the writer's own rule for a catalogue document
// (internal/cli.FindCatalogues): `catalogue.yaml` or `catalogue-<name>.yaml`.
func isCatalogueFile(name string) bool {
	return !strings.ContainsAny(name, "/\\") && strings.HasPrefix(name, "catalogue") && strings.HasSuffix(name, ".yaml")
}

// isYAMLFile is any document a catalogue directory may hold a catalogue in.
func isYAMLFile(name string) bool {
	return !strings.ContainsAny(name, "/\\") && name != ".yaml" && strings.HasSuffix(name, ".yaml")
}

// checkLooksLikeCatalogue is the preview's check of a document taken as a
// catalogue only because it was the one .yaml of a directory, or a file given
// under another name: a stray values.yaml fails here, naming the file, and not at
// the writer's start, where it would drain the ingest queue into the dead-letter
// queue. A catalogue has a source, a version and actions (sdk/schemas/catalogue.schema.json).
func checkLooksLikeCatalogue(where, file, doc string) error {
	var c struct {
		Source  string         `yaml:"source"`
		Version string         `yaml:"version"`
		Actions map[string]any `yaml:"actions"`
	}
	if err := yaml.Unmarshal([]byte(doc), &c); err != nil {
		return fmt.Errorf("auditpulumi: %s: %s is not YAML, so it is not a catalogue: %w", where, file, err)
	}
	if c.Source == "" || c.Version == "" || len(c.Actions) == 0 {
		return fmt.Errorf("auditpulumi: %s: %s is taken as the catalogue because it is the one .yaml here, and is not one: "+
			"a catalogue has a source, a version and actions. Name the catalogue catalogue.yaml or catalogue-<name>.yaml, "+
			"or keep other .yaml files out of the directory", where, file)
	}
	return nil
}

// layerName is the name a catalogue document has in the writer's layer: the
// writer only finds `catalogue.yaml` and `catalogue-<name>.yaml`, so a document
// from a file named otherwise (`shop.yaml`) is shipped as `catalogue-shop.yaml`.
// An application need not name its file for the writer's sake.
func layerName(file string) string {
	if isCatalogueFile(file) {
		return file
	}
	return "catalogue-" + file
}

// isSchemaFile is a data schema's file name: what LoadFS reads beside a
// catalogue.
func isSchemaFile(name string) bool {
	return !strings.ContainsAny(name, "/\\") && name != ".json" && strings.HasSuffix(name, ".json")
}

// readCatalogueDirs merges each of w.CatalogueDirs into Catalogues and
// CatalogueSchemas, as CataloguePaths is merged into Catalogues: a name given
// twice must have the same content.
func readCatalogueDirs(w *WriterArgs) error {
	if len(w.CatalogueDirs) == 0 {
		return nil
	}
	docs := make(map[string]string, len(w.Catalogues)+len(w.CatalogueDirs))
	for k, v := range w.Catalogues {
		docs[k] = v
	}
	schemas := make(map[string]map[string]string, len(w.CatalogueSchemas)+len(w.CatalogueDirs))
	for k, v := range w.CatalogueSchemas {
		schemas[k] = v
	}
	for _, dir := range w.CatalogueDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("auditpulumi: Writer.CatalogueDirs: %w", err)
		}
		// A directory's catalogue is the one `catalogue.yaml` or `catalogue-<name>.yaml`
		// it holds, as before, and other .yaml files in it are not read. A directory
		// with none of those holds its catalogue in the one .yaml it has, whatever it
		// is called: the library ships it under a name the writer finds.
		var doc string
		var named, other []string
		found := map[string]string{}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			switch {
			case isCatalogueFile(name):
				named = append(named, name)
			case isYAMLFile(name):
				other = append(other, name)
			case isSchemaFile(name):
				body, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					return fmt.Errorf("auditpulumi: Writer.CatalogueDirs: %w", err)
				}
				found[name] = string(body)
			}
		}
		switch {
		case len(named) > 1:
			return fmt.Errorf("auditpulumi: Writer.CatalogueDirs: %s holds %s and %s: a directory holds one catalogue, "+
				"because the writer gives every .json beside a catalogue to it", dir, named[0], named[1])
		case len(named) == 1:
			doc = named[0]
		case len(other) == 1:
			doc = other[0]
		case len(other) > 1:
			return fmt.Errorf("auditpulumi: Writer.CatalogueDirs: %s holds %s and %s and neither is named catalogue.yaml or "+
				"catalogue-<name>.yaml: a directory holds one catalogue, so name the one that is", dir, other[0], other[1])
		default:
			return fmt.Errorf("auditpulumi: Writer.CatalogueDirs: %s holds no catalogue: a .yaml file, such as catalogue.yaml", dir)
		}
		body, err := os.ReadFile(filepath.Join(dir, doc))
		if err != nil {
			return fmt.Errorf("auditpulumi: Writer.CatalogueDirs: %w", err)
		}
		if strings.TrimSpace(string(body)) == "" {
			return fmt.Errorf("auditpulumi: Writer.CatalogueDirs: %s is empty", filepath.Join(dir, doc))
		}
		if !isCatalogueFile(doc) {
			if err := checkLooksLikeCatalogue("Writer.CatalogueDirs", filepath.Join(dir, doc), string(body)); err != nil {
				return err
			}
		}
		doc = layerName(doc)
		if prev, ok := docs[doc]; ok && prev != string(body) {
			return fmt.Errorf("auditpulumi: Writer.CatalogueDirs: %s is also in Writer.Catalogues with other content", doc)
		}
		docs[doc] = string(body)
		if prev, ok := schemas[doc]; ok && !sameFiles(prev, found) {
			return fmt.Errorf("auditpulumi: Writer.CatalogueDirs: the schemas of %s are also in Writer.CatalogueSchemas with other content", doc)
		}
		if len(found) > 0 {
			schemas[doc] = found
		}
	}
	w.Catalogues, w.CatalogueSchemas = docs, schemas
	return nil
}

func sameFiles(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// checkCatalogueSchemas holds each catalogue to the writer's own start-up check,
// before anything is created: every schema a catalogue references is supplied,
// and every schema supplied is referenced. A writer that fails it never starts,
// and the event source mapping then drains the ingest queue into the dead-letter
// queue while the deploy reports success.
func checkCatalogueSchemas(w *WriterArgs) error {
	for file := range w.CatalogueSchemas {
		if _, ok := w.Catalogues[file]; !ok {
			return fmt.Errorf("auditpulumi: Writer.CatalogueSchemas names %s, which is not one of the catalogues", file)
		}
	}
	files := make([]string, 0, len(w.Catalogues))
	for f := range w.Catalogues {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, file := range files {
		var problems []error
		refs, err := schemaReferences(file, w.Catalogues[file])
		if err != nil {
			return err
		}
		supplied := map[string]string{}
		names := make([]string, 0, len(w.CatalogueSchemas[file]))
		for name := range w.CatalogueSchemas[file] {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if !isSchemaFile(name) {
				return fmt.Errorf("auditpulumi: Writer.CatalogueSchemas[%s] has %q: a schema file is named <name>.json", file, name)
			}
			var s struct {
				ID string `json:"$id"`
			}
			if err := json.Unmarshal([]byte(w.CatalogueSchemas[file][name]), &s); err != nil {
				return fmt.Errorf("auditpulumi: Writer.CatalogueSchemas[%s] %s is not a JSON object: %w", file, name, err)
			}
			if s.ID == "" {
				return fmt.Errorf("auditpulumi: Writer.CatalogueSchemas[%s] %s has no $id: a catalogue references a schema by it", file, name)
			}
			id := canonicalSchemaID(s.ID)
			if prev, ok := supplied[id]; ok {
				return fmt.Errorf("auditpulumi: Writer.CatalogueSchemas[%s]: %s and %s both claim %s", file, prev, name, s.ID)
			}
			supplied[id] = name
		}
		for _, r := range refs {
			if _, ok := supplied[canonicalSchemaID(r.id)]; !ok {
				problems = append(problems, fmt.Errorf("%s references schema %s, which was not supplied", r.where, r.id))
			}
		}
		used := map[string]bool{}
		for _, r := range refs {
			used[canonicalSchemaID(r.id)] = true
		}
		for id, name := range supplied {
			if !used[id] {
				problems = append(problems, fmt.Errorf("schema %s (%s) is supplied but nothing references it", id, name))
			}
		}
		if len(problems) > 0 {
			return fmt.Errorf("auditpulumi: Writer.Catalogues %s would stop the writer at start-up, and every record would go to the "+
				"dead-letter queue: give the catalogue's data schemas with Writer.CatalogueDirs or Writer.CatalogueSchemas:\n%w",
				file, errors.Join(problems...))
		}
	}
	return nil
}

type schemaRef struct{ where, id string }

// schemaReferences are the schema ids a catalogue document names, where the
// writer looks for them (sdk/catalogue Catalogue.check).
func schemaReferences(file, doc string) ([]schemaRef, error) {
	var c struct {
		ActorKinds map[string]struct {
			AttributesSchema string `yaml:"attributes_schema"`
		} `yaml:"actor_kinds"`
		TargetTypes map[string]struct {
			AttributesSchema string `yaml:"attributes_schema"`
		} `yaml:"target_types"`
		ContextAreas map[string]string `yaml:"context_areas"`
		Meters       map[string]struct {
			DimensionsSchema string `yaml:"dimensions_schema"`
		} `yaml:"meters"`
		Actions map[string]struct {
			DataSchema string `yaml:"data_schema"`
		} `yaml:"actions"`
	}
	if err := yaml.Unmarshal([]byte(doc), &c); err != nil {
		return nil, fmt.Errorf("auditpulumi: Writer.Catalogues %s is not YAML: %w", file, err)
	}
	var out []schemaRef
	add := func(where, id string) {
		if id != "" {
			out = append(out, schemaRef{where, id})
		}
	}
	for n, k := range c.ActorKinds {
		add("actor kind "+n, k.AttributesSchema)
	}
	for n, t := range c.TargetTypes {
		add("target type "+n, t.AttributesSchema)
	}
	for area, id := range c.ContextAreas {
		add("context area "+area, id)
	}
	for n, m := range c.Meters {
		add("meter "+n, m.DimensionsSchema)
	}
	for n, a := range c.Actions {
		add("action "+n, a.DataSchema)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].where < out[j].where })
	return out, nil
}

// canonicalSchemaID is sdk/catalogue.CanonicalID: an id under the legacy base
// names the same schema as the one under the published base.
func canonicalSchemaID(id string) string {
	if rest, ok := strings.CutPrefix(id, "https://schemas.truvity.com/audit/v1/"); ok {
		return "https://truvity.github.io/audit/schemas/v1/" + rest
	}
	return id
}

// catalogueLayerFiles are the catalogues' files in the writer's layer, by path
// under /opt/audit.
func catalogueLayerFiles(w *WriterArgs) (map[string]string, error) {
	out := map[string]string{}
	for file, body := range w.Catalogues {
		if !isCatalogueFile(file) {
			return nil, fmt.Errorf("auditpulumi: Writer.Catalogues has %q: a catalogue file is named catalogue.yaml or catalogue-<name>.yaml, "+
				"which is what the writer looks for", file)
		}
		schemas := w.CatalogueSchemas[file]
		if len(schemas) == 0 {
			out[cataloguesDir+"/"+file] = body
			continue
		}
		dir := cataloguesDir + "/" + strings.TrimSuffix(file, ".yaml")
		out[dir+"/"+file] = body
		for name, s := range schemas {
			out[dir+"/"+name] = s
		}
	}
	return out, nil
}
