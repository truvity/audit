package writer_test

import (
	"strings"
	"testing"

	"github.com/truvity/audit/internal/writer"
	"github.com/truvity/audit/preset"
)

// A deployment without keys has to say what it means, because the alternative
// is writing whatever arrives into an archive nothing can edit.
func TestGuardKeysRefusesPseudonymsWithNoProvider(t *testing.T) {
	profiles := map[string]*preset.Profile{
		"security": {Name: "security", Identity: map[preset.Category]preset.Treatment{
			preset.External: preset.Pseudonym,
		}},
	}
	err := writer.GuardKeys(profiles, false)
	if err == nil {
		t.Fatal("a profile that pseudonymises started without a key provider")
	}
	for _, want := range []string{"security", "external_identifiers_are_opaque"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should mention %q: %v", want, err)
		}
	}
}

// Declaring the identifiers opaque is one of the two answers, and Compose is
// where it takes effect, so the guard sees `clear` and has nothing to refuse.
func TestGuardKeysAcceptsWhatTheDeploymentDeclaredOpaque(t *testing.T) {
	presets, err := preset.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	d := &preset.Deployment{
		Profiles:                     map[string]preset.ProfileConfig{"security": {Presets: []string{"security"}}},
		ExternalIdentifiersAreOpaque: true,
	}
	profiles, err := d.Compose(presets)
	if err != nil {
		t.Fatal(err)
	}
	if got := profiles["security"].Identity[preset.External]; got != preset.Clear {
		t.Fatalf("external treatment = %q, want %q once the deployment declares them opaque", got, preset.Clear)
	}
	if !profiles["security"].OpaqueExternal {
		t.Error("the profile should remember that the treatment was relaxed, so explaining it can say why")
	}
	if err := writer.GuardKeys(profiles, false); err != nil {
		t.Errorf("a deployment that declared its identifiers opaque was refused: %v", err)
	}
}

// With a provider there is nothing to decide.
func TestGuardKeysAcceptsAProvider(t *testing.T) {
	profiles := map[string]*preset.Profile{
		"security": {Name: "security", Identity: map[preset.Category]preset.Treatment{
			preset.External: preset.Pseudonym,
		}},
	}
	if err := writer.GuardKeys(profiles, true); err != nil {
		t.Errorf("a deployment with a key provider was refused: %v", err)
	}
}
