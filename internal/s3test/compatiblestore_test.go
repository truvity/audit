package s3test

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/config"

	"github.com/truvity/audit/store"
	"github.com/truvity/audit/store/s3store"
)

// CompatibleStoreEnv names a bucket on a store that is NOT AWS, and its
// endpoint, that this check may write to.
//
// WHY IT CANNOT BE AN EMULATOR. What it checks is a signature: the SDK signs
// a put one way when it is asked for a checksum ALGORITHM and the object also
// carries a Content-Encoding, and a store that is not AWS may verify that
// signature differently. Cloudflare R2 answers 403 SignatureDoesNotMatch, and
// every record object this archive writes is zstd-encoded, so on R2 that was
// every record -- while the uncompressed schema objects beside them wrote
// fine, which is what made it look like anything but a signing problem. No
// emulator reproduces it, because an emulator's job is to accept what the SDK
// sends.
//
// The unit test is store/s3store: the store must send the checksum VALUE and
// never the algorithm. This is the same claim against a real store.
//
//	AUDIT_S3_COMPATIBLE_BUCKET=<bucket> AUDIT_S3_COMPATIBLE_ENDPOINT=<url> \
//	  go test ./internal/s3test -run CompatibleStore
const (
	CompatibleStoreEnv         = "AUDIT_S3_COMPATIBLE_BUCKET"
	CompatibleStoreEndpointEnv = "AUDIT_S3_COMPATIBLE_ENDPOINT"
)

func TestCompatibleStoreTakesACompressedRecord(t *testing.T) {
	bucket, endpoint := os.Getenv(CompatibleStoreEnv), os.Getenv(CompatibleStoreEndpointEnv)
	if bucket == "" || endpoint == "" {
		t.Skip("set " + CompatibleStoreEnv + " and " + CompatibleStoreEndpointEnv +
			" to a bucket on a store that is not AWS to run the compatibility check")
	}
	ctx := context.Background()
	// `auto` is what a store with one region answers to; the SDK insists on
	// having one whatever the store does with it.
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("auto"))
	if err != nil {
		t.Fatal(err)
	}
	archive, err := s3store.FromConfig(cfg, s3store.Options{
		Bucket: bucket, Prefix: "audit-compatibility-check", Endpoint: endpoint,
		// A store without the Object Lock API is the reason to be here.
		Lock: s3store.None,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Shaped like a record object: the partition key an archive writes, zstd,
	// and the metadata the roller attaches. The tenant carries an `@` because
	// the platform's own does.
	key := "profile=security/tenant=@platform/" + strings.ToLower(rand.Text()[:12]) + ".ndjson.zst"
	body := []byte("a compressed record")
	if err := archive.Put(ctx, store.Object{
		Key:         key,
		Body:        body,
		ContentType: "application/x-ndjson",
		Encoding:    "zstd",
		Metadata:    map[string]string{"audit-profile": "security", "audit-tenant": "@platform"},
	}); err != nil {
		t.Fatalf("putting a compressed record: %v", err)
	}
	got, err := archive.Get(ctx, key)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("read back %d bytes, wrote %d", len(got), len(body))
	}
}
