// Package cli is the implementation behind cmd/audit. It is internal because
// the command is the contract, not the Go API of its subcommands.
package cli

import (
	"fmt"
	"os"

	"sigs.k8s.io/yaml"

	"github.com/truvity/audit/preset"
)

// Deployment is what a deployment declares about its profiles: which presets
// each is composed from and where its copies land. It is the same document the
// chart renders and the writer reads.
type Deployment struct {
	Profiles map[string]ProfileConfig `json:"profiles"`
}

// ProfileConfig is one profile's composition.
type ProfileConfig struct {
	Presets []string `json:"presets"`
	Prefix  string   `json:"prefix,omitempty"`
}

// LoadDeployment reads a deployment's profile configuration.
func LoadDeployment(path string) (*Deployment, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var d Deployment
	if err := yaml.UnmarshalStrict(raw, &d); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(d.Profiles) == 0 {
		return nil, fmt.Errorf("%s: no profiles", path)
	}
	return &d, nil
}

// Compose resolves every profile a deployment declares.
func (d *Deployment) Compose(presets map[string]*preset.Preset) (map[string]*preset.Profile, error) {
	out := make(map[string]*preset.Profile, len(d.Profiles))
	for name, c := range d.Profiles {
		p, err := preset.Compose(preset.Composition{Name: name, Presets: c.Presets, Prefix: c.Prefix}, presets)
		if err != nil {
			return nil, err
		}
		out[name] = p
	}
	return out, nil
}

// DefaultDeployment is what a deployment gets when it declares nothing: one
// profile per preset this repository ships, named for the preset. It is a
// starting point for `profile explain`, not a recommendation.
func DefaultDeployment(presets map[string]*preset.Preset) *Deployment {
	d := &Deployment{Profiles: map[string]ProfileConfig{}}
	for name := range presets {
		d.Profiles[name] = ProfileConfig{Presets: []string{name}}
	}
	return d
}
