package observe_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/truvity/audit/internal/observe"
	"github.com/truvity/audit/internal/writer"
	"github.com/truvity/audit/sdk/catalogue"
	"github.com/truvity/audit/sdk/record"
	"github.com/truvity/audit/store/storetest"
)

// What describes a record is in the bucket that holds it, from before the first
// record that names it: the writer puts the catalogue and its extension schemas
// there, and observe resolves a record's indexed properties from that copy and
// from nothing it has to be configured with.
func TestFieldsAreResolvedFromTheArchivesOwnCatalogue(t *testing.T) {
	ctx := context.Background()
	shop, err := catalogue.LoadFS(os.DirFS("../../examples/emit/catalogue"), "shop.yaml")
	if err != nil {
		t.Fatal(err)
	}
	archive := storetest.NewMemory()
	if err := (&writer.SchemaArchive{Store: archive}).EnsureCatalogue(ctx, shop); err != nil {
		t.Fatal(err)
	}

	fields := observe.FieldsFrom(&observe.ArchiveCatalogues{Store: archive})
	r := &record.Record{Source: "shop", CatalogueVersion: "1.0.0", Action: "shop.order.placed"}
	got, err := fields(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got.Facet, "/channel") || !slices.Contains(got.Filter, "/channel") {
		t.Fatalf("the properties the schema marks are not indexed: %+v", got)
	}

	// A record whose catalogue is not there is an error and never an empty
	// answer: an index built without its data columns could not be repaired by
	// a later run, which would count each record once.
	missing := &record.Record{Source: "shop", CatalogueVersion: "2.0.0", Action: "shop.order.placed"}
	if _, err := fields(ctx, missing); !errors.Is(err, observe.ErrNoCatalogue) {
		t.Fatalf("a record of a catalogue that is not in the archive: %v", err)
	}
}

// A catalogue the operator names wins over the archive's, and the archive is
// asked only for what the files do not hold.
func TestAGivenCatalogueIsTriedBeforeTheArchives(t *testing.T) {
	ctx := context.Background()
	shop, err := catalogue.LoadFS(os.DirFS("../../examples/emit/catalogue"), "shop.yaml")
	if err != nil {
		t.Fatal(err)
	}
	archive := storetest.NewMemory() // holds nothing
	chain := observe.Chain{observe.Given{}.With(shop), &observe.ArchiveCatalogues{Store: archive}}
	if _, err := chain.Get(ctx, "shop", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if archive.Gets != 0 {
		t.Errorf("the archive was read %d times for a catalogue that was given", archive.Gets)
	}
	if _, err := chain.Get(ctx, "shop", "9.9.9"); !errors.Is(err, observe.ErrNoCatalogue) {
		t.Fatalf("an unknown version: %v", err)
	}
}

// A record written under a former name of its source is indexed under the
// current one, and the archive resolves the former name to the same catalogue,
// so a rebuild reads old records and new alike.
func TestARecordUnderAFormerSourceNameIsIndexedUnderTheCurrentOne(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile("../../examples/emit/catalogue/shop.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.Replace(string(raw), "source: shop\n", "source: store\naliases: [shop]\n", 1)
	doc = strings.ReplaceAll(doc, "shop.", "store.")
	schema, err := os.ReadFile("../../examples/emit/catalogue/order-placed.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := catalogue.Load([]byte(doc), [][]byte{schema})
	if err != nil {
		t.Fatal(err)
	}
	archive := storetest.NewMemory()
	if err := (&writer.SchemaArchive{Store: archive}).EnsureCatalogue(ctx, c); err != nil {
		t.Fatal(err)
	}

	from := &observe.ArchiveCatalogues{Store: archive}
	fields := observe.FieldsFrom(from)
	old := &record.Record{Source: "shop", CatalogueVersion: "1.0.0", Action: "shop.order.placed"}
	got, err := fields(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "store" || got.Action != "store.order.placed" {
		t.Fatalf("indexed under %q, %q", got.Source, got.Action)
	}
	current := &record.Record{Source: "store", CatalogueVersion: "1.0.0", Action: "store.order.placed"}
	if got, err = fields(ctx, current); err != nil || got.Source != "" {
		t.Fatalf("a record under the current name is rewritten: %+v, %v", got, err)
	}

	// The query side learns the aliases from the archive.
	names, err := from.Aliased(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := names.CanonicalSource("shop"); got != "store" {
		t.Fatalf("the archive's aliases resolve shop to %q", got)
	}
}
