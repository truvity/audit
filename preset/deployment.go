package preset

import (
	"errors"
	"fmt"

	"sigs.k8s.io/yaml"
)

// Deployment is what a deployment declares about its profiles: which presets
// each is composed from and where its copies land. It is the document the
// chart renders and the writer reads, and an application embedding a writer
// reads the same one, so that its profiles are declared the way a standalone
// deployment's are.
type Deployment struct {
	Profiles map[string]ProfileConfig `json:"profiles"`
}

// ProfileConfig is one profile's composition.
type ProfileConfig struct {
	Presets []string `json:"presets"`
	Prefix  string   `json:"prefix,omitempty"`
}

// ParseDeployment reads a deployment document. Unknown keys are refused: a
// misspelt field in a document that decides retention is not one to ignore.
func ParseDeployment(raw []byte) (*Deployment, error) {
	var d Deployment
	if err := yaml.UnmarshalStrict(raw, &d); err != nil {
		return nil, fmt.Errorf("deployment: %w", err)
	}
	if len(d.Profiles) == 0 {
		return nil, errors.New("deployment: no profiles")
	}
	return &d, nil
}

// Compose resolves every profile a deployment declares.
func (d *Deployment) Compose(presets map[string]*Preset) (map[string]*Profile, error) {
	out := make(map[string]*Profile, len(d.Profiles))
	for name, c := range d.Profiles {
		p, err := Compose(Composition{Name: name, Presets: c.Presets, Prefix: c.Prefix}, presets)
		if err != nil {
			return nil, err
		}
		out[name] = p
	}
	return out, nil
}

// DefaultDeployment is what a deployment gets when it declares nothing: one
// profile per preset, named for the preset. It is a starting point for
// looking at what the presets keep, not a recommendation.
func DefaultDeployment(presets map[string]*Preset) *Deployment {
	d := &Deployment{Profiles: map[string]ProfileConfig{}}
	for name := range presets {
		d.Profiles[name] = ProfileConfig{Presets: []string{name}}
	}
	return d
}
