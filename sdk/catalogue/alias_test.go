package catalogue

import (
	"strings"
	"testing"
)

func renamedWallet(t *testing.T) *Catalogue {
	t.Helper()
	doc := strings.ReplaceAll(walletDoc, "wallet", "purse")
	doc = strings.Replace(doc, "version:", "aliases: [wallet]\nversion:", 1)
	c, err := Load([]byte(doc), [][]byte{[]byte(strings.ReplaceAll(walletSchema, "wallet", "purse"))})
	if err != nil {
		t.Fatalf("the renamed catalogue does not load: %v", err)
	}
	return c
}

func TestAliasesAreFormerNamesOfTheSource(t *testing.T) {
	c := renamedWallet(t)
	if got := c.CanonicalSource("wallet"); got != "purse" {
		t.Errorf("CanonicalSource(wallet) = %q", got)
	}
	if got := c.CanonicalSource("purse"); got != "purse" {
		t.Errorf("CanonicalSource(purse) = %q", got)
	}
	if got := c.CanonicalSource("other"); got != "other" {
		t.Errorf("a name that is neither is %q", got)
	}
	if got := c.CanonicalAction("wallet.credential.issued"); got != "purse.credential.issued" {
		t.Errorf("CanonicalAction = %q", got)
	}
	if !c.Answers("wallet") || !c.Answers("purse") || c.Answers("other") {
		t.Error("Answers is wrong")
	}
	if _, ok := c.Action("wallet.credential.issued"); !ok {
		t.Error("an action under the former name is not found")
	}
}

func TestARecordUnderTheFormerNameValidates(t *testing.T) {
	c := renamedWallet(t)
	x, err := c.Compose("wallet.credential.issued")
	if err != nil {
		t.Fatal(err)
	}
	if x.Name != "purse.credential.issued" {
		t.Fatalf("composed as %q", x.Name)
	}
	r := issued(t) // written under wallet
	if err := x.Validate(r); err != nil {
		t.Fatalf("a record under the former name was refused: %v", err)
	}
	r.Source = "stranger"
	if err := x.Validate(r); err == nil {
		t.Fatal("a record under a name that is no alias was accepted")
	}
}

func TestLoadRefusesABadAlias(t *testing.T) {
	for name, aliases := range map[string]string{
		"the source itself": "[wallet]",
		"a duplicate":       "[old, old]",
		"not a name":        "[Not_A_Name]",
	} {
		doc := strings.Replace(walletDoc, "version:", "aliases: "+aliases+"\nversion:", 1)
		if err := loadWith(t, doc, walletSchema); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

func TestNamesResolveAcrossCatalogues(t *testing.T) {
	n := Names{wallet(t), renamedWallet(t)}
	if got := n.CanonicalSource("wallet"); got != "purse" {
		t.Errorf("CanonicalSource = %q", got)
	}
	if got := n.CanonicalAction("wallet.credential.issued"); got != "purse.credential.issued" {
		t.Errorf("CanonicalAction = %q", got)
	}
	if got := n.CanonicalSource("shop"); got != "shop" {
		t.Errorf("CanonicalSource(shop) = %q", got)
	}
}
