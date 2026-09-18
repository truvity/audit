package main

import (
	"testing"

	"github.com/truvity/audit/catalogue"
)

// The example's catalogue is one the toolchain accepts, so the guide never
// shows a catalogue that would be refused.
func TestTheExampleCatalogueLoads(t *testing.T) {
	document, err := files.ReadFile("catalogue/shop.yaml")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := files.ReadFile("catalogue/order-placed.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalogue.Load(document, [][]byte{schema}); err != nil {
		t.Fatal(err)
	}
}
