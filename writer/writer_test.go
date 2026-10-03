package writer_test

import (
	"context"
	"strings"
	"testing"

	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/store/storetest"
	"github.com/truvity/audit/writer"
)

func profiles(t *testing.T) map[string]*preset.Profile {
	t.Helper()
	return compose(t, "profiles:\n  security:\n    presets: [security]\n")
}

// opaqueProfiles is the same deployment declaring that the identifiers it
// receives for people outside the organisation are ones an application
// minted. Nothing is pseudonymised, so nothing needs a key.
func opaqueProfiles(t *testing.T) map[string]*preset.Profile {
	t.Helper()
	return compose(t, "external_identifiers_are_opaque: true\nprofiles:\n  security:\n    presets: [security]\n")
}

func compose(t *testing.T, document string) map[string]*preset.Profile {
	t.Helper()
	d, err := preset.ParseDeployment([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	presets, err := preset.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.Compose(presets)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A writer missing a part refuses to open, and says which part, rather than
// writing identifiers in clear or keeping nothing.
func TestOpenRefusesAnIncompleteWriter(t *testing.T) {
	ctx := context.Background()
	provider, err := keys.NewLocal(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	full := writer.Config{Archive: storetest.NewMemory(), Profiles: profiles(t), Keys: provider}
	for name, c := range map[string]struct {
		config writer.Config
		says   string
	}{
		"no archive":  {writer.Config{Profiles: full.Profiles, Keys: provider}, "archive"},
		"no profiles": {writer.Config{Archive: full.Archive, Keys: provider}, "profile"},
		// Keys are refused only because this profile pseudonymises; see the
		// test below for the deployment that needs none.
		"no keys for a profile that pseudonymises": {
			writer.Config{Archive: full.Archive, Profiles: full.Profiles}, "key provider",
		},
		"replicas without a shared table": {writer.Config{
			Archive: full.Archive, Profiles: full.Profiles, Keys: provider, Replicas: 2,
		}, "replica"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := writer.Open(ctx, c.config); err == nil || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("got %v, want a refusal naming %q", err, c.says)
			}
		})
	}
	w, err := writer.Open(ctx, full)
	if err != nil {
		t.Fatalf("a complete writer: %v", err)
	}
	if err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(ctx); err != nil {
		t.Fatalf("closing twice: %v", err)
	}
}

// A deployment whose profiles pseudonymise nobody needs no key provider, and
// opens without one.
//
// This is the ordinary case, not an exotic one: an organisation's own trail
// keeps its own people in clear because that is what accountability is for,
// and the identifiers it holds for anyone else are ones an application
// minted. Asked for keys anyway, such a deployment has nothing to give and
// the writer never starts -- which is how 0.2.x shipped, with a leftover
// requirement in front of the guard that decides this properly.
func TestAWriterThatPseudonymisesNobodyNeedsNoKeys(t *testing.T) {
	ctx := context.Background()
	w, err := writer.Open(ctx, writer.Config{
		Archive:  storetest.NewMemory(),
		Profiles: opaqueProfiles(t),
	})
	if err != nil {
		t.Fatalf("a writer with no keys and nothing to pseudonymise: %v", err)
	}
	if err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

// The catalogue the writer runs with is at catalogue/<app>/<version>, written
// when it starts; a second writer starting over the same bytes carries on, and
// one whose catalogue of that version is something else refuses to run.
func TestTheWriterWritesItsCatalogueAtStartAndRefusesAConflictingOne(t *testing.T) {
	ctx := context.Background()
	archive := storetest.NewMemory()
	open := func() (*writer.Writer, error) {
		return writer.Open(ctx, writer.Config{Archive: archive, Profiles: opaqueProfiles(t)})
	}

	w, err := open()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}
	var key string
	for _, k := range archive.Keys() {
		if strings.HasPrefix(k, "catalogue/") {
			key = k
		}
	}
	if key == "" {
		t.Fatalf("no catalogue was written at start: %v", archive.Keys())
	}

	// Another instance over the same bucket finds the same bytes and starts.
	w, err = open()
	if err != nil {
		t.Fatalf("a second writer over the same catalogue: %v", err)
	}
	if err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// The bucket holds another document under that version: refuse to run.
	archive.Replace(key, []byte("another document under the same version"))
	if _, err := open(); err == nil || !strings.Contains(err.Error(), "different catalogue") {
		t.Fatalf("a writer started over a conflicting catalogue: %v", err)
	}
}
