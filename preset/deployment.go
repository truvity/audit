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
	// ExternalIdentifiersAreOpaque is the deployment saying that the
	// identifiers it receives for people outside the organisation are already
	// pseudonyms: identifiers an application minted, which name nobody without
	// that application's own database.
	//
	// Where it is true, a profile asking for `external: pseudonym` gets
	// `clear`, because encrypting an opaque identifier a second time adds a key
	// to lose and tells a reader of the archive nothing new. The writer holds
	// the deployment to it, refusing a record whose external identifier looks
	// direct.
	//
	// It defaults to false, so a deployment arrives at clear identifiers by
	// saying so and not by omission.
	ExternalIdentifiersAreOpaque bool `json:"external_identifiers_are_opaque,omitempty"`
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
//
// This is where ExternalIdentifiersAreOpaque takes effect, so that what a
// profile says it keeps is what it keeps: `audit profile explain` prints the
// composed profile, and a treatment the deployment has relaxed should not be
// something a reader has to know to subtract.
func (d *Deployment) Compose(presets map[string]*Preset) (map[string]*Profile, error) {
	out := make(map[string]*Profile, len(d.Profiles))
	for name, c := range d.Profiles {
		p, err := Compose(Composition{Name: name, Presets: c.Presets, Prefix: c.Prefix}, presets)
		if err != nil {
			return nil, err
		}
		if d.ExternalIdentifiersAreOpaque && p.Identity[External] == Pseudonym {
			p.Identity[External] = Clear
			p.OpaqueExternal = true
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
