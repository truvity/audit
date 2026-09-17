package catalogue

import "testing"

// The catalogue this repository ships is the first thing the toolchain is held
// to: if the component cannot describe its own events, it has no business
// demanding that applications describe theirs.
func TestCommonCatalogueLoads(t *testing.T) {
	c, err := Common()
	if err != nil {
		t.Fatalf("the common catalogue does not load: %v", err)
	}
	if c.Source != "audit" {
		t.Fatalf("source = %q, want audit", c.Source)
	}
	for _, want := range []string{
		"audit.search", "audit.export.requested", "audit.catalogue.registered",
		"audit.key.destroyed", "audit.digest.written", "audit.clock.synchronised",
		"audit.writer.dead_lettered",
	} {
		if _, ok := c.Action(want); !ok {
			t.Errorf("the common catalogue declares no %s", want)
		}
	}
	// Reading the trail is itself an event; without these the component could
	// not answer who looked.
	cats := c.Categories()
	for _, want := range []string{"log_access", "logging_control", "clock", "key_lifecycle"} {
		if !cats[want] {
			t.Errorf("the common catalogue covers no %s category", want)
		}
	}
}

func TestCommonCatalogueActionsCompose(t *testing.T) {
	c, err := Common()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range c.ActionNames() {
		if _, err := c.Compose(name); err != nil {
			t.Errorf("%s does not compose: %v", name, err)
		}
	}
}
