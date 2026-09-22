package writer_test

import (
	"strings"
	"testing"

	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/internal/writer"
)

// A catalogue that asks for a property to be hashed needs a key provider, the
// same as an identifier does. Without one the writer would take every such
// record and dead-letter it, which a deployment finds out on the day it
// matters. It refuses to start instead, naming the property.
func TestGuardHashesRefusesWithNoProvider(t *testing.T) {
	c, err := catalogue.Load([]byte(walletDoc), [][]byte{[]byte(walletSchema)})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Hashes()) == 0 {
		t.Fatal("the fixture hashes nothing, so this proves nothing")
	}

	err = writer.GuardHashes([]*catalogue.Catalogue{c}, false)
	if err == nil {
		t.Fatal("a catalogue that hashes started without a key provider")
	}
	for _, want := range []string{c.Source, "hashed", "key provider"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should mention %q: %v", want, err)
		}
	}
	if err := writer.GuardHashes([]*catalogue.Catalogue{c}, true); err != nil {
		t.Errorf("a deployment with a provider was refused: %v", err)
	}
}
