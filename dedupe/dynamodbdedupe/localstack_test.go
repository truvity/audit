package dynamodbdedupe_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/truvity/audit/dedupe/dynamodbdedupe"
)

// urlEnv names the DynamoDB endpoint these tests run against: the LocalStack
// the s3 tests use. They skip when it is unset, so the hermetic suite stays
// hermetic.
const urlEnv = "AUDIT_DYNAMODB_URL"

// TestLocalStackConditionalPut runs the store against a real DynamoDB API, which
// is the one thing the in-memory fake cannot vouch for: that the condition and
// the projection expressions are ones DynamoDB accepts, and that a second mark
// of a live id is refused by the condition and not by luck.
func TestLocalStackConditionalPut(t *testing.T) {
	endpoint := os.Getenv(urlEnv)
	if endpoint == "" {
		t.Skipf("%s is not set", urlEnv)
	}
	client := dynamodb.New(dynamodb.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
	})
	var b [6]byte
	_, _ = rand.Read(b[:])
	table := "audit-dedupe-" + hex.EncodeToString(b[:])
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            aws.String(table),
		BillingMode:          types.BillingModePayPerRequest,
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String(dynamodbdedupe.KeyAttribute), AttributeType: types.ScalarAttributeTypeS}},
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String(dynamodbdedupe.KeyAttribute), KeyType: types.KeyTypeHash}},
	}); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() {
		_, _ = client.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(table)})
	})

	now := time.Now()
	d, err := dynamodbdedupe.New(client, table, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	d.Now = func() time.Time { return now }
	if seen, err := d.Seen(ctx, []string{"r1"}); err != nil || len(seen) != 0 {
		t.Fatalf("Seen on a new table = %v, %v", seen, err)
	}
	if err := d.Mark(ctx, []string{"r1", "r2"}); err != nil {
		t.Fatalf("Mark: %v", err)
	}
	if err := d.Mark(ctx, []string{"r1"}); err != nil {
		t.Fatalf("a second Mark of a live id failed: %v", err)
	}
	seen, err := d.Seen(ctx, []string{"r1", "r2", "r3"})
	if err != nil || !seen["r1"] || !seen["r2"] || seen["r3"] {
		t.Fatalf("Seen = %v, %v", seen, err)
	}
	now = now.Add(2 * time.Hour)
	if seen, err = d.Seen(ctx, []string{"r1"}); err != nil || seen["r1"] {
		t.Fatalf("an expired id was seen: %v, %v", seen, err)
	}
	if err := d.Mark(ctx, []string{"r1"}); err != nil {
		t.Fatalf("re-marking an expired id: %v", err)
	}
	if seen, _ = d.Seen(ctx, []string{"r1"}); !seen["r1"] {
		t.Fatal("re-marked id not seen")
	}
}
