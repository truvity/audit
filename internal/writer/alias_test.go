package writer_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/truvity/audit/internal/writer"
	"github.com/truvity/audit/sdk/catalogue"
)

// walletRenamed is the wallet catalogue after the source was renamed to
// purse, with the old name kept as an alias.
func walletRenamed() string {
	doc := strings.Replace(walletDoc, "source: wallet", "source: purse\naliases: [wallet]", 1)
	return strings.Replace(doc, "  wallet.credential.issued:", "  purse.credential.issued:", 1)
}

// A record written under the former name of a source is accepted, as is one
// under the current name, and both land in the archive as they were written.
func TestAWriterAcceptsARecordUnderEitherNameOfASource(t *testing.T) {
	b := buildWith(t, parts{doc: walletRenamed()})

	old := fresh(t) // names wallet, the former name
	current := fresh(t)
	current.Source = "purse"
	current.Action = "purse.credential.issued"
	res := write(t, b, old, current)

	if res.Accepted != 2 || len(b.deadLetter) != 0 {
		t.Fatalf("accepted %d, dead letters %v", res.Accepted, b.deadLetter)
	}
	seen := map[string]bool{}
	for _, c := range decode(t, b.store) {
		seen[c.GetSource()+" "+c.GetAction()] = true
	}
	for _, want := range []string{"wallet wallet.credential.issued", "purse purse.credential.issued"} {
		if !seen[want] {
			t.Errorf("no record %q in the archive; have %v", want, seen)
		}
	}
}

// An alias is a name for this source and for nothing else: a record under a
// name the catalogue does not list is still refused.
func TestAWriterStillRefusesAnUnlistedName(t *testing.T) {
	b := buildWith(t, parts{doc: walletRenamed()})
	r := fresh(t)
	r.Source = "stranger"
	write(t, b, r)
	if len(b.deadLetter) != 1 {
		t.Fatalf("dead letters = %v", b.deadLetter)
	}
}

// The archive keeps the catalogue under its source and under each alias, so a
// record's own source and version find what describes it.
func TestTheArchiveKeepsACatalogueUnderItsAliases(t *testing.T) {
	a, s := archive(t)
	c, err := catalogue.Load([]byte(walletRenamed()), [][]byte{[]byte(walletSchema)})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.EnsureCatalogue(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"purse", "wallet"} {
		held, err := s.Get(context.Background(), "catalogue/"+name+"/1.0.0")
		if err != nil || string(held) != walletRenamed() {
			t.Fatalf("catalogue/%s/1.0.0: %v", name, err)
		}
	}
}

// An alias does not make a version new: the former name already holds that
// version with another document, so the writer refuses to run.
func TestAnAliasWhoseVersionHoldsAnotherDocumentIsAConflict(t *testing.T) {
	a, _ := archive(t)
	old, err := catalogue.Load([]byte(walletDoc), [][]byte{[]byte(walletSchema)})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.EnsureCatalogue(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	renamed, err := catalogue.Load([]byte(walletRenamed()), [][]byte{[]byte(walletSchema)})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.EnsureCatalogue(context.Background(), renamed); !errors.Is(err, writer.ErrCatalogueConflict) {
		t.Fatalf("an alias over a version held with other bytes: %v", err)
	}
}

// A catalogue of the former name that was registered itself is exact: an alias
// never shadows it.
func TestAnAliasDoesNotShadowACatalogueOfThatName(t *testing.T) {
	old, err := catalogue.Load([]byte(walletDoc), [][]byte{[]byte(walletSchema)})
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := catalogue.Load([]byte(walletRenamed()), [][]byte{[]byte(walletSchema)})
	if err != nil {
		t.Fatal(err)
	}
	for name, order := range map[string][]*catalogue.Catalogue{
		"alias first": {renamed, old}, "exact first": {old, renamed},
	} {
		r := &writer.Registry{}
		for _, c := range order {
			r.Register(c)
		}
		got, err := r.Get(context.Background(), "wallet", "1.0.0")
		if err != nil || got.Source != "wallet" {
			t.Errorf("%s: resolved %v, %v", name, got, err)
		}
	}
}

func TestTheRegistryResolvesAnAliasToTheCurrentCatalogue(t *testing.T) {
	renamed, err := catalogue.Load([]byte(walletRenamed()), [][]byte{[]byte(walletSchema)})
	if err != nil {
		t.Fatal(err)
	}
	r := &writer.Registry{}
	r.Register(renamed)
	got, err := r.Get(context.Background(), "wallet", "1.0.0")
	if err != nil || got.Source != "purse" {
		t.Fatalf("former name resolved to %v, %v", got, err)
	}
}
