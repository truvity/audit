package writer_test

import (
	"context"
	"strings"
	"testing"

	"github.com/truvity/audit/internal/identity"
	"github.com/truvity/audit/keys"
)

// A copy that pseudonymises a person keeps the way back, sealed, beside it:
// the security profile pseudonymises the external holder, and the pseudonym it
// wrote resolves to the holder it replaced.
func TestAPseudonymisedCopyKeepsTheWayBack(t *testing.T) {
	b := buildWith(t, parts{keepIdentities: true})
	write(t, b, fresh(t))

	var pseudonym string
	for _, c := range decode(t, b.store) {
		if c.GetProfile() == "security" {
			pseudonym = c.GetSubject().GetId()
		}
	}
	if pseudonym == "" || pseudonym == "alice" {
		t.Fatalf("the security copy's subject is %q, not a pseudonym", pseudonym)
	}
	for _, key := range b.store.Keys() {
		if strings.HasPrefix(key, identity.Prefix+"/") {
			body, _ := b.store.Get(context.Background(), key)
			if strings.Contains(string(body), "alice") {
				t.Fatalf("%s keeps the identity in clear", key)
			}
		}
	}
	got, err := b.identities.Resolve(context.Background(), "acme", keys.Purpose("security"), pseudonym)
	if err != nil || got != "alice" {
		t.Fatalf("resolve: %q %v", got, err)
	}
}
