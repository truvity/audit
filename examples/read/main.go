// Command read is an example of reading the audit trail from Go: a search,
// paged to the end with its cursor, and one record read with where it came
// from. It is the code docs/guides/read.md walks through, compiled on every
// run of the gate so that the guide cannot drift from the API.
//
// The query service authenticates a bearer token from an issuer it trusts;
// what the caller may see comes from its grants file:
//
//	AUDIT_QUERY=https://audit-query.example.com \
//	AUDIT_BEARER=$(your-sign-in-tool token) \
//	go run ./examples/read
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/gen/audit/v1/auditv1connect"
)

func main() {
	if err := run(); err != nil {
		slog.Error("read", "error", err)
		os.Exit(1)
	}
}

// bearer adds the caller's token to every request.
type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func run() error {
	ctx := context.Background()
	client := auditv1connect.NewQueryServiceClient(
		&http.Client{Transport: bearer{os.Getenv("AUDIT_BEARER")}},
		os.Getenv("AUDIT_QUERY"),
	)

	// Failed and denied credential actions in the last day, newest first.
	// The filter is OR-joined conjunctions of typed predicates; the grant is
	// AND-ed in by the service, so a caller never sees outside it.
	query := &auditv1.SearchRequest{
		Profile: "security",
		Filter: []*auditv1.Filter{{
			OccurredAt: &auditv1.TimePredicate{Operator: &auditv1.TimePredicate_Between{Between: &auditv1.TimeRange{
				From: timestamppb.New(time.Now().Add(-24 * time.Hour)),
				To:   timestamppb.Now(),
			}}},
			Action:  &auditv1.StringPredicate{Operator: &auditv1.StringPredicate_Prefix{Prefix: "shop.order."}},
			Outcome: &auditv1.StringPredicate{Operator: &auditv1.StringPredicate_In{In: &auditv1.StringList{Values: []string{"failure", "denied"}}}},
		}},
		Limit: 100,
	}

	// Page with the cursor until there is no more. The last page's `next`
	// cursor is kept: asked again later, it returns what was recorded since,
	// which is what a tail is.
	var first string
	for {
		page, err := client.Search(ctx, connect.NewRequest(query))
		if err != nil {
			return err
		}
		for _, r := range page.Msg.GetItems() {
			fmt.Printf("%s %s %s %s\n", r.GetOccurredAt().AsTime().Format(time.RFC3339),
				r.GetAction(), r.GetActor().GetId(), r.GetOutcome().GetResult())
			if first == "" {
				first = r.GetId()
			}
		}
		next := page.Msg.GetCursors().GetNext()
		if len(page.Msg.GetItems()) < int(query.GetLimit()) || next == "" {
			break
		}
		query.Cursor = next
	}
	if first == "" {
		return nil
	}

	// One record, with where the copy lives and whether the digest chain has
	// vouched for it.
	got, err := client.Get(ctx, connect.NewRequest(&auditv1.GetRequest{Profile: "security", Id: first}))
	if err != nil {
		return err
	}
	p := got.Msg.GetProvenance()
	fmt.Printf("%s is line %d of %s; digest %q, verified %s\n",
		first, p.GetLine(), p.GetObjectKey(), p.GetDigestId(), p.GetVerifiedAt().AsTime().Format(time.RFC3339))
	return nil
}
