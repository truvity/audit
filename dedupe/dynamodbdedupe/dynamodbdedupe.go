// Package dynamodbdedupe is the writer's record of what it has written, kept in
// a DynamoDB table, for a writer that has no database: a function platform,
// where the writer is a Lambda and Postgres is somebody else's service.
//
// It is the Dedupe port of the writer (see internal/writer) and has the same
// contract as the Postgres one: asking marks nothing, and the writer marks only
// once the copies are durable, so a crash between the put and the mark can only
// cost a second copy and never a hole. The semantics are those of a JetStream
// duplicate window, kept by id for a window, with no more machinery than that:
//
//   - one item per written record id, `pk = "DEDUPE#<id>"`, and an `expires_at`
//     in epoch seconds;
//   - Mark is a conditional put per id: it writes only where there is no item or
//     the item has expired, so two writers marking one id at once are not an
//     error, and a repeat does not extend the window;
//   - Seen is a consistent batch read that treats an expired item as absent,
//     because DynamoDB deletes expired items lazily, up to two days late, and a
//     writer must not be told "seen" about a record whose window is over;
//   - the table's TTL attribute is `expires_at`, so the table cleans itself and
//     Purge has nothing to do.
//
// The table is the deployment's to create (deploy/pulumi does): a hash key `pk`
// of type string, on-demand billing, TTL on `expires_at`. The writer's role
// needs GetItem/BatchGetItem and PutItem on it and nothing else.
package dynamodbdedupe

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/smithy-go"
)

// Attribute names of the table. They are part of the contract with the
// deployment: deploy/pulumi creates the table with these.
const (
	KeyAttribute = "pk"
	TTLAttribute = "expires_at"
	// KeyPrefix is what every item's key starts with, so one table can hold
	// other things without a collision.
	KeyPrefix = "DEDUPE#"
)

// maxGet is how many keys BatchGetItem takes.
const maxGet = 100

// DefaultWindow is how long an id is remembered when none is given: the Postgres
// store's default.
const DefaultWindow = 14 * 24 * time.Hour

// API is the part of the DynamoDB client this package calls.
type API interface {
	BatchGetItem(ctx context.Context, in *dynamodb.BatchGetItemInput,
		opts ...func(*dynamodb.Options)) (*dynamodb.BatchGetItemOutput, error)
	PutItem(ctx context.Context, in *dynamodb.PutItemInput,
		opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
}

// Dedupe is a deduplication store over a DynamoDB table.
type Dedupe struct {
	API   API
	Table string
	// Window is how long an id is remembered. Default 14 days. It wants to be at
	// least as long as the longest redelivery the queue allows: the queue's
	// retention, which on SQS is at most 14 days.
	Window time.Duration
	// Now is the clock, for tests.
	Now func() time.Time
}

// New returns a store over a table.
func New(api API, table string, window time.Duration) (*Dedupe, error) {
	switch {
	case api == nil:
		return nil, errors.New("dynamodbdedupe: a DynamoDB client is required")
	case table == "":
		return nil, errors.New("dynamodbdedupe: a table name is required")
	}
	return &Dedupe{API: api, Table: table, Window: window}, nil
}

// Seen implements the writer's Dedupe. It reads and marks nothing.
func (d *Dedupe) Seen(ctx context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	wanted := distinct(ids)
	now := d.now()
	for len(wanted) > 0 {
		n := min(len(wanted), maxGet)
		chunk := wanted[:n]
		wanted = wanted[n:]
		keys := make([]map[string]types.AttributeValue, len(chunk))
		for i, id := range chunk {
			keys[i] = key(id)
		}
		request := map[string]types.KeysAndAttributes{
			d.Table: {Keys: keys, ConsistentRead: aws.Bool(true), ProjectionExpression: aws.String("#pk, #ttl"),
				ExpressionAttributeNames: map[string]string{"#pk": KeyAttribute, "#ttl": TTLAttribute}},
		}
		// DynamoDB may answer for part of a batch and name the rest unprocessed;
		// a repeat for them is how a throttled read is completed.
		for attempt := 0; len(request) > 0; attempt++ {
			if attempt > 5 {
				return nil, fmt.Errorf("dynamodbdedupe: DynamoDB left keys unprocessed after %d attempts", attempt)
			}
			if attempt > 0 {
				if err := sleep(ctx, time.Duration(attempt)*50*time.Millisecond); err != nil {
					return nil, err
				}
			}
			res, err := d.API.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{RequestItems: request})
			if err != nil {
				return nil, fmt.Errorf("dynamodbdedupe: reading: %w", err)
			}
			for _, item := range res.Responses[d.Table] {
				id, live := idOf(item, now)
				if live {
					out[id] = true
				}
			}
			request = res.UnprocessedKeys
		}
	}
	return out, nil
}

// Mark implements the writer's Dedupe: a conditional put per id, which writes
// where there is no item or the item has expired. An item already there is
// success, not a failure: a second writer marked the same id first.
func (d *Dedupe) Mark(ctx context.Context, ids []string) error {
	now := d.now()
	expires := now.Add(d.window()).Unix()
	var errs []error
	for _, id := range distinct(ids) {
		item := key(id)
		item[TTLAttribute] = &types.AttributeValueMemberN{Value: strconv.FormatInt(expires, 10)}
		_, err := d.API.PutItem(ctx, &dynamodb.PutItemInput{
			TableName:                aws.String(d.Table),
			Item:                     item,
			ConditionExpression:      aws.String("attribute_not_exists(#pk) OR #ttl < :now"),
			ExpressionAttributeNames: map[string]string{"#pk": KeyAttribute, "#ttl": TTLAttribute},
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":now": &types.AttributeValueMemberN{Value: strconv.FormatInt(now.Unix(), 10)},
			},
		})
		if err != nil && !conditionFailed(err) {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("dynamodbdedupe: marking %d of %d records as written failed: %w",
			len(errs), len(distinct(ids)), errors.Join(errs...))
	}
	return nil
}

// Purge does nothing: the table's TTL deletes what has expired.
func (d *Dedupe) Purge(context.Context, time.Time) error { return nil }

func key(id string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{KeyAttribute: &types.AttributeValueMemberS{Value: KeyPrefix + id}}
}

// idOf reads an item back: the id, and whether its window is still open.
func idOf(item map[string]types.AttributeValue, now time.Time) (string, bool) {
	pk, ok := item[KeyAttribute].(*types.AttributeValueMemberS)
	if !ok || len(pk.Value) <= len(KeyPrefix) {
		return "", false
	}
	id := pk.Value[len(KeyPrefix):]
	n, ok := item[TTLAttribute].(*types.AttributeValueMemberN)
	if !ok {
		// No expiry is no window to be inside of: an item the TTL would never
		// delete is one this package did not write, and it is believed.
		return id, true
	}
	exp, err := strconv.ParseInt(n.Value, 10, 64)
	if err != nil {
		return id, true
	}
	return id, exp >= now.Unix()
}

func conditionFailed(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "ConditionalCheckFailedException"
	}
	var cc *types.ConditionalCheckFailedException
	return errors.As(err, &cc)
}

func distinct(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func (d *Dedupe) window() time.Duration {
	if d.Window > 0 {
		return d.Window
	}
	return DefaultWindow
}

func (d *Dedupe) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

func sleep(ctx context.Context, dur time.Duration) error {
	t := time.NewTimer(dur)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
