package dynamodbdedupe_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/smithy-go"

	"github.com/truvity/audit/dedupe/dynamodbdedupe"
)

// fake is a DynamoDB table in memory, with the two behaviours the package
// leans on: a conditional put that refuses an item that is there, and a batch
// read that returns part of what was asked and names the rest unprocessed.
type fake struct {
	items map[string]string // pk -> expires_at
	// reads counts BatchGetItem calls; sizes records how many keys each asked for.
	sizes []int
	// partial, when set, makes the first read answer for half its keys.
	partial bool
	failPut error
}

func (f *fake) BatchGetItem(_ context.Context, in *dynamodb.BatchGetItemInput,
	_ ...func(*dynamodb.Options)) (*dynamodb.BatchGetItemOutput, error) {
	out := &dynamodb.BatchGetItemOutput{Responses: map[string][]map[string]types.AttributeValue{}}
	for table, ka := range in.RequestItems {
		f.sizes = append(f.sizes, len(ka.Keys))
		keys := ka.Keys
		if f.partial && len(keys) > 1 {
			f.partial = false
			half := len(keys) / 2
			out.UnprocessedKeys = map[string]types.KeysAndAttributes{table: {Keys: keys[half:], ConsistentRead: ka.ConsistentRead,
				ProjectionExpression: ka.ProjectionExpression, ExpressionAttributeNames: ka.ExpressionAttributeNames}}
			keys = keys[:half]
		}
		for _, k := range keys {
			pk := k[dynamodbdedupe.KeyAttribute].(*types.AttributeValueMemberS).Value
			if exp, ok := f.items[pk]; ok {
				item := map[string]types.AttributeValue{dynamodbdedupe.KeyAttribute: &types.AttributeValueMemberS{Value: pk}}
				if exp != "" {
					item[dynamodbdedupe.TTLAttribute] = &types.AttributeValueMemberN{Value: exp}
				}
				out.Responses[table] = append(out.Responses[table], item)
			}
		}
	}
	return out, nil
}

func (f *fake) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	if f.failPut != nil {
		return nil, f.failPut
	}
	pk := in.Item[dynamodbdedupe.KeyAttribute].(*types.AttributeValueMemberS).Value
	now, _ := strconv.ParseInt(in.ExpressionAttributeValues[":now"].(*types.AttributeValueMemberN).Value, 10, 64)
	if old, ok := f.items[pk]; ok {
		exp, _ := strconv.ParseInt(old, 10, 64)
		if exp >= now { // the condition: no item, or an expired one
			return nil, &types.ConditionalCheckFailedException{}
		}
	}
	f.items[pk] = in.Item[dynamodbdedupe.TTLAttribute].(*types.AttributeValueMemberN).Value
	return &dynamodb.PutItemOutput{}, nil
}

func newStore(t *testing.T, f *fake, now *time.Time) *dynamodbdedupe.Dedupe {
	t.Helper()
	d, err := dynamodbdedupe.New(f, "audit", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	d.Now = func() time.Time { return *now }
	return d
}

func TestAskingMarksNothingAndMarkingIsSeen(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	f := &fake{items: map[string]string{}}
	d := newStore(t, f, &now)
	ctx := context.Background()

	seen, err := d.Seen(ctx, []string{"a", "b"})
	if err != nil || len(seen) != 0 {
		t.Fatalf("Seen on an empty table = %v, %v; want nothing", seen, err)
	}
	if len(f.items) != 0 {
		t.Fatalf("asking marked %v", f.items)
	}
	if err := d.Mark(ctx, []string{"a", "a", "", "b"}); err != nil {
		t.Fatal(err)
	}
	if len(f.items) != 2 {
		t.Fatalf("a repeat and a blank were marked: %v", f.items)
	}
	seen, err = d.Seen(ctx, []string{"a", "c"})
	if err != nil || !seen["a"] || seen["c"] || len(seen) != 1 {
		t.Fatalf("Seen = %v, %v; want only a", seen, err)
	}
	if _, ok := f.items[dynamodbdedupe.KeyPrefix+"a"]; !ok {
		t.Fatalf("the key is not prefixed with %s: %v", dynamodbdedupe.KeyPrefix, f.items)
	}
}

func TestAWindowThatIsOverIsNotSeenBeforeDynamoDBDeletesIt(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	f := &fake{items: map[string]string{}}
	d := newStore(t, f, &now)
	ctx := context.Background()
	if err := d.Mark(ctx, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour) // past the window; the item is still in the table
	seen, err := d.Seen(ctx, []string{"a"})
	if err != nil || seen["a"] {
		t.Fatalf("an id whose window is over was reported seen: %v, %v", seen, err)
	}
	// And it can be marked again, which opens a new window.
	if err := d.Mark(ctx, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if seen, _ = d.Seen(ctx, []string{"a"}); !seen["a"] {
		t.Fatal("re-marking after expiry did not open a new window")
	}
}

func TestMarkingAnIdAnotherWriterMarkedIsNotAFailure(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	f := &fake{items: map[string]string{}}
	d := newStore(t, f, &now)
	ctx := context.Background()
	if err := d.Mark(ctx, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	before := f.items[dynamodbdedupe.KeyPrefix+"a"]
	now = now.Add(time.Minute)
	if err := d.Mark(ctx, []string{"a"}); err != nil {
		t.Fatalf("a repeat mark failed: %v", err)
	}
	if f.items[dynamodbdedupe.KeyPrefix+"a"] != before {
		t.Fatal("a repeat extended the window")
	}
}

func TestAMarkThatFailsIsAnError(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	f := &fake{items: map[string]string{}, failPut: &smithy.GenericAPIError{Code: "ProvisionedThroughputExceededException"}}
	d := newStore(t, f, &now)
	if err := d.Mark(context.Background(), []string{"a"}); err == nil {
		t.Fatal("a put DynamoDB refused was reported as marked")
	}
	f.failPut = errors.New("boom")
	if err := d.Mark(context.Background(), []string{"a"}); err == nil {
		t.Fatal("a failed put was reported as marked")
	}
	// A condition failure carried as a generic API error is still one.
	f.failPut = &smithy.GenericAPIError{Code: "ConditionalCheckFailedException"}
	if err := d.Mark(context.Background(), []string{"a"}); err != nil {
		t.Fatalf("a refused condition was a failure: %v", err)
	}
}

func TestReadsAreBatchedAndUnprocessedKeysAreCompleted(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	f := &fake{items: map[string]string{}, partial: true}
	d := newStore(t, f, &now)
	ctx := context.Background()
	ids := make([]string, 250)
	for i := range ids {
		ids[i] = fmt.Sprintf("id-%03d", i)
	}
	if err := d.Mark(ctx, ids); err != nil {
		t.Fatal(err)
	}
	seen, err := d.Seen(ctx, append(ids, "never"))
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 250 || seen["never"] {
		t.Fatalf("seen %d ids, want 250 and not the one never written", len(seen))
	}
	for _, n := range f.sizes {
		if n > 100 {
			t.Fatalf("a read asked for %d keys; DynamoDB takes 100", n)
		}
	}
}

func TestTheStoreRefusesWhatItCannotUse(t *testing.T) {
	if _, err := dynamodbdedupe.New(nil, "t", 0); err == nil {
		t.Fatal("no client was accepted")
	}
	if _, err := dynamodbdedupe.New(&fake{}, "", 0); err == nil {
		t.Fatal("no table was accepted")
	}
}
