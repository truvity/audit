package keys_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/truvity/audit/keys"
)

// The OpenBAO tests need a server: AUDIT_OPENBAO_URL and AUDIT_OPENBAO_TOKEN,
// as a dev server gives them (`bao server -dev`). A transit signer tested
// against a stand-in would prove only that it called the API it meant to.
const (
	openbaoURLEnv   = "AUDIT_OPENBAO_URL"
	openbaoTokenEnv = "AUDIT_OPENBAO_TOKEN"
)

func openbao(t *testing.T) (string, string) {
	t.Helper()
	url, token := os.Getenv(openbaoURLEnv), os.Getenv(openbaoTokenEnv)
	if url == "" || token == "" {
		t.Skip("set " + openbaoURLEnv + " and " + openbaoTokenEnv + " to run the transit tests (an OpenBAO dev server)")
	}
	return url, token
}

// bao makes one call to the server, for the set-up a deployment does by hand.
func bao(t *testing.T, url, token, method, path string, body any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(method, url+"/v1/"+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Vault-Token", token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode/100 != 2 && res.StatusCode != http.StatusBadRequest {
		t.Fatalf("%s %s: %s", method, path, res.Status)
	}
}

// transitKey mounts transit once and creates a fresh key of the given type.
func transitKey(t *testing.T, url, token, kind string) string {
	t.Helper()
	// Mounting twice answers 400, which is fine: it is mounted.
	bao(t, url, token, http.MethodPost, "sys/mounts/transit", map[string]string{"type": "transit"})
	// Unique per run: a dev server outlives a test run, and a key left from
	// the last one has been rotated already.
	name := "digest-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")) + "-" + strings.ToLower(rand.Text()[:6])
	bao(t, url, token, http.MethodPost, "transit/keys/"+name, map[string]string{"type": kind})
	return name
}

// A signature from transit verifies with only the exported public half, and
// the signer names the key version it signed with.
func TestATransitSignatureVerifiesWithItsPublicHalf(t *testing.T) {
	url, token := openbao(t)
	name := transitKey(t, url, token, "ed25519")
	ctx := context.Background()
	s, err := keys.NewTransitSigner(ctx, &keys.TransitSigner{Address: url, Key: name, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.KeyID(); got != "transit:"+name+":v1" {
		t.Fatalf("key id %q", got)
	}
	message := []byte(`{"digest_version":"1","objects":[]}`)
	signature, err := s.Sign(ctx, message)
	if err != nil {
		t.Fatal(err)
	}
	public, err := s.PublicKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.Verify(public, message, signature); err != nil {
		t.Fatalf("a transit signature did not verify: %v", err)
	}
	if err := keys.Verify(public, append(message, ' '), signature); err == nil {
		t.Fatal("a signature verified over a message it was not made for")
	}
}

// After the key is rotated a signer that has read it keeps signing with the
// version it exported, and a new one names the new version — so the public
// half and the signatures always belong together.
func TestATransitSignerStaysOnTheVersionItExported(t *testing.T) {
	url, token := openbao(t)
	name := transitKey(t, url, token, "ed25519")
	ctx := context.Background()
	before, err := keys.NewTransitSigner(ctx, &keys.TransitSigner{Address: url, Key: name, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	bao(t, url, token, http.MethodPost, "transit/keys/"+name+"/rotate", map[string]string{})

	message := []byte("window")
	signature, err := before.Sign(ctx, message)
	if err != nil {
		t.Fatal(err)
	}
	public, _ := before.PublicKey(ctx)
	if err := keys.Verify(public, message, signature); err != nil {
		t.Fatalf("after a rotation the signer drifted from the key it exported: %v", err)
	}
	after, err := keys.NewTransitSigner(ctx, &keys.TransitSigner{Address: url, Key: name, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	if after.KeyID() != "transit:"+name+":v2" {
		t.Fatalf("a new signer after rotation names %q", after.KeyID())
	}
}

// A key of another type is refused before anything is signed.
func TestATransitKeyOfTheWrongTypeIsRefused(t *testing.T) {
	url, token := openbao(t)
	name := transitKey(t, url, token, "aes256-gcm96")
	_, err := keys.NewTransitSigner(context.Background(), &keys.TransitSigner{Address: url, Key: name, Token: token})
	if err == nil || !strings.Contains(err.Error(), "ed25519") {
		t.Fatalf("an encryption key was accepted as a signer: %v", err)
	}
}
