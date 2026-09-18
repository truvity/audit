// Command emit is an example application that records what it does into the
// audit trail. It is the code docs/guides/emit.md walks through, compiled on
// every run of the gate so that the guide cannot drift from the library.
//
// It needs a writer and a registry, and a token that proves which workload it
// is — in a cluster, its projected service-account token:
//
//	AUDIT_WRITER=http://audit:8080 \
//	AUDIT_REGISTRY=http://audit-registry:8080 \
//	AUDIT_TOKEN_FILE=/var/run/audit/token \
//	go run ./examples/emit
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/truvity/audit/auth"
	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/emit"
	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

// The catalogue ships with the code that emits it, so the two change together.
//
//go:embed catalogue
var files embed.FS

func main() {
	if err := run(); err != nil {
		slog.Error("shop", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	// 1. Load the catalogue and its extension schemas.
	document, err := files.ReadFile("catalogue/shop.yaml")
	if err != nil {
		return err
	}
	schema, err := files.ReadFile("catalogue/order-placed.json")
	if err != nil {
		return err
	}
	shop, err := catalogue.Load(document, [][]byte{schema})
	if err != nil {
		return err
	}

	// 2. Every call carries this workload's identity. In a cluster that is the
	// projected service-account token; the file is read on every request
	// because the kubelet replaces it before it expires.
	client := http.DefaultClient
	if path := os.Getenv("AUDIT_TOKEN_FILE"); path != "" {
		client = auth.TokenFile(path)
	}

	// 3. Register the catalogue with the deployment, and do not start if it is
	// refused: records written against a description nobody accepted are
	// records nobody can read.
	if err := emit.Register(ctx, emit.Registration{
		URL:    os.Getenv("AUDIT_REGISTRY"),
		Source: shop.Source, Version: shop.Version,
		Document: document,
		Schemas:  map[string][]byte{"https://schemas.example.com/shop/order-placed.json": schema},
		HTTP:     client,
	}); err != nil {
		return err
	}

	// 4. The emitter: bound to the catalogue, delivering to the writer.
	emitter, err := emit.New(emit.Options{
		Source:    shop.Source,
		Catalogue: shop,
		Sink:      sink.NewClient(client, os.Getenv("AUDIT_WRITER")),
		Version:   "1.0.0",
		Instance:  os.Getenv("HOSTNAME"),
		Hooks: emit.Hooks{
			// best_effort actions may drop under pressure; a deployment that
			// does not hear about it has no idea what it is missing.
			OnDropped: func(r *record.Record, reason string) {
				slog.Error("audit record dropped", "action", r.GetAction(), "reason", reason)
			},
		},
	})
	if err != nil {
		return err
	}
	defer emitter.Close() //nolint:errcheck // shutting down

	// 5. The handler records the action. Middleware captures the client
	// address, user agent and request and trace ids for every record made
	// while serving the request; 1 is how many proxies of your own sit in
	// front.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		order := placeOrder()
		data, _ := structpb.NewStruct(map[string]any{"items": float64(order.Items), "channel": "web"})
		err := emitter.Record(r.Context(), &record.Record{
			Action:    "shop.order.placed",
			Operation: auditv1.Operation_OPERATION_CREATE,
			TenantId:  "acme", // the customer organisation this happened for
			Actor:     &record.Actor{Kind: "customer", Id: customerID(r)},
			Targets:   []*record.Target{{Type: "order", Id: order.ID}},
			Outcome:   &record.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS},
			Data:      data,
		})
		if err != nil {
			// The action is declared block: an order the trail did not take
			// is an order that did not happen.
			http.Error(w, "the order could not be recorded", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(order)
	})

	server := &http.Server{Addr: ":8080", Handler: emit.Middleware(1)(mux), ReadHeaderTimeout: 10 * time.Second}
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

type order struct {
	ID    string `json:"id"`
	Items int    `json:"items"`
}

func placeOrder() order { return order{ID: record.NewID(), Items: 2} }

// customerID is whoever the application authenticated. The writer pseudonymises
// it in the profiles that ask for that; the application records it as it is.
func customerID(r *http.Request) string { return r.Header.Get("X-Customer") }
