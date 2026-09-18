package writer_test

import (
	"context"
	"crypto/rand"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/store/storetest"
)

// Two writers that share nothing but the transit engine — separate stores,
// separate filesystems — give the same person the same pseudonym. It is the
// property the local provider only has on a shared directory, and the reason
// a deployment of several replicas wants transit.
func TestTwoWritersOnTransitAgreeWithoutSharingADirectory(t *testing.T) {
	url, token := os.Getenv("AUDIT_OPENBAO_URL"), os.Getenv("AUDIT_OPENBAO_TOKEN")
	if url == "" || token == "" {
		t.Skip("set AUDIT_OPENBAO_URL and AUDIT_OPENBAO_TOKEN to run against an OpenBAO dev server")
	}
	req, _ := http.NewRequest(http.MethodPost, url+"/v1/sys/mounts/transit", strings.NewReader(`{"type":"transit"}`))
	req.Header.Set("X-Vault-Token", token)
	if res, err := http.DefaultClient.Do(req); err == nil {
		_ = res.Body.Close()
	}
	prefix := "audit-" + strings.ToLower(rand.Text()[:8])

	subject := func(instance string) string {
		t.Helper()
		provider, err := keys.NewTransit(context.Background(), &keys.Transit{Address: url, Token: token, Prefix: prefix})
		if err != nil {
			t.Fatal(err)
		}
		s := storetest.NewMemory()
		b := buildWith(t, parts{store: s, instance: instance, provider: provider})
		write(t, b, fresh(t))
		for _, r := range decode(t, s) {
			if r.GetProfile() == "security" && r.GetAction() == "wallet.credential.issued" {
				return r.GetSubject().GetId()
			}
		}
		t.Fatal("no security copy was written")
		return ""
	}
	one, two := subject("writer-1"), subject("writer-2")
	if one != two || !keys.IsPseudonym(one) {
		t.Fatalf("two writers on one engine gave %q and %q", one, two)
	}
}
