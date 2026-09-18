package emit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/gen/audit/v1/auditv1connect"
)

// Registration is a catalogue an application publishes about itself.
type Registration struct {
	// URL is the registry service.
	URL string
	// Source and Version must match the document, and Source must match the
	// identity the transport gives the registry: an application registers its
	// own catalogue and no other's.
	Source, Version string
	// Document is the catalogue, and Schemas are the extension schemas it
	// references, keyed by their $id.
	Document []byte
	Schemas  map[string][]byte
	// HTTP is the client to use. A deployment puts its own in, because this is
	// the call that carries the identity the registry checks.
	HTTP connect.HTTPClient
}

// ErrCatalogueRefused is a registration the deployment answered and refused.
// It is a fact about the catalogue, and trying again will not change it; any
// other error from Register is about reaching the registry, and may.
var ErrCatalogueRefused = errors.New("emit: the deployment refused the catalogue")

// Register publishes a catalogue and fails on rejection.
//
// An application calls this at start-up and does not start if the catalogue is
// refused (ErrCatalogueRefused). That is the point of it: a catalogue the
// deployment rejected — because it is malformed, or registered under a source
// the caller does not own — describes records the application is about to
// write, and writing them against a description nothing accepted is how an
// archive ends up holding records nobody can read. A registry that cannot be
// reached is different: an application whose records wait in an outbox may
// start and register again later.
//
// Registering the same catalogue again is not an error. Every replica does it
// on every roll.
func Register(ctx context.Context, r Registration) error {
	switch {
	case r.URL == "":
		return fmt.Errorf("emit: a registry URL is required")
	case r.Source == "" || r.Version == "":
		return fmt.Errorf("emit: a registration must name its source and version")
	case len(r.Document) == 0:
		return fmt.Errorf("emit: a registration must carry the catalogue document")
	}
	client := r.HTTP
	if client == nil {
		client = http.DefaultClient
	}

	res, err := auditv1connect.NewRegistryServiceClient(client, r.URL).
		RegisterCatalogue(ctx, connect.NewRequest(&auditv1.RegisterCatalogueRequest{
			Catalogue: &auditv1.Catalogue{
				Source: r.Source, Version: r.Version,
				Document: r.Document, Schemas: r.Schemas,
			},
		}))
	if err != nil {
		return fmt.Errorf("emit: registering %s %s: %w", r.Source, r.Version, err)
	}
	if problems := res.Msg.GetProblems(); len(problems) > 0 {
		return fmt.Errorf("%w: %s %s:\n  %s",
			ErrCatalogueRefused, r.Source, r.Version, strings.Join(problems, "\n  "))
	}
	return nil
}
