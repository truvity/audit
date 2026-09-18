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
	d, err := preset.ParseDeployment([]byte("profiles:\n  security:\n    presets: [security]\n"))
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
		"no keys":     {writer.Config{Archive: full.Archive, Profiles: full.Profiles}, "key provider"},
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
