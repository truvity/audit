// Package query is the query service as a library: search, facets, get, export,
// tail and resolve over what the writer wrote, behind the grants a caller holds,
// with every read recorded in the trail it reads.
//
// This is what the audit-query binary runs: that binary is built on this
// package. It is exported because the binary needs it and a test may, not as a
// way to deploy — the query service of an installation is its own Deployment,
// beside the writer.
//
//	q, err := query.New(query.Config{
//		Searcher:      postgres.NewReader(pool), // or &s3scan.Scanner{Store: archive}
//		Archive:       archive,
//		Authenticator: session,   // the application's own sign-in
//		Authorizer:    grants,    // e.g. auth.AccessRoster, or auth.Declarative
//		Sink:          w,         // the writer: every read is recorded
//	})
//	path, handler := q.Handler()
//	mux.Handle(path, handler)
package query

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/truvity/audit/auth"
	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/index"
	"github.com/truvity/audit/internal/identity"
	inner "github.com/truvity/audit/internal/query"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/sink"
	"github.com/truvity/audit/store"
)

// Config is what the service needs. Searcher, Authenticator, Authorizer and
// Sink are required.
type Config struct {
	// Searcher answers the questions: postgres.NewReader over the index, as a
	// role that does not own the tables, or an s3scan.Scanner over the archive.
	Searcher index.Searcher
	// Authenticator says who is calling, from the request.
	Authenticator auth.Authenticator
	// Authorizer says what they may read. There is no default: a service that
	// answered without one would answer everything.
	Authorizer auth.Authorizer
	// Sink is where every read is recorded: the installation's writer.
	// Reading an audit trail is itself an auditable event, and a service that
	// records none is half a service.
	Sink sink.Sink

	// Archive, when given, lets Get say which digest covers a record and when
	// it was last verified, and is where resolve finds sealed identities.
	Archive store.Store
	// Keys, when given together with Archive, offers resolve: the way back
	// from a pseudonym to the identity behind it. It must open what the
	// writer sealed — the same keys, or a role on them that may decrypt.
	// Without it, resolve is refused whatever a grant says.
	Keys keys.Sealer
	// Exports, when given, offers export.
	Exports *Exports

	Version  string
	Instance string
	// Logger, default slog.Default(). A read that happened and was not
	// recorded is logged there as an error; a deployment alerts on it.
	Logger *slog.Logger
}

// Exports is where exports are written and how they are collected. The store
// is not the archive: an export is an unlocked copy meant to be cleared, and
// the archive's policy denies every delete.
type Exports struct {
	Store     store.Store
	Presigner store.Presigner
	// Expiry is how long an export is kept. Default 7 days.
	Expiry time.Duration
	// LinkValid is how long a download link works. Default one hour.
	LinkValid time.Duration
	// MaxRecords bounds one export. Default 100000.
	MaxRecords int
}

// Service is a running query service.
type Service struct {
	inner         *inner.Service
	authenticator auth.Authenticator
}

// New checks the configuration and prepares the service's own emitter.
func New(c Config) (*Service, error) {
	switch {
	case c.Authenticator == nil:
		return nil, errors.New("query: an authenticator is required: a service must know who is asking")
	case c.Sink == nil:
		return nil, errors.New(
			"query: a sink is required: reading an audit trail is itself an auditable event")
	case c.Keys != nil && c.Archive == nil:
		return nil, errors.New("query: resolve needs the archive, where the sealed identities are kept")
	}
	log := c.Logger
	if log == nil {
		log = slog.Default()
	}
	common, err := catalogue.Common()
	if err != nil {
		return nil, err
	}
	s := &inner.Service{
		Searcher:   c.Searcher,
		Authorizer: c.Authorizer,
		Sink:       c.Sink,
		Catalogue:  common,
		Archive:    c.Archive,
		Version:    c.Version,
		Instance:   c.Instance,
		OnUnrecorded: func(action string, err error) {
			// Not a degraded service: this is the service failing at one of
			// the two things it is for.
			log.Error("a read was not recorded", "action", action, "error", err)
		},
	}
	if c.Keys != nil {
		s.Identities = &identity.Map{Store: c.Archive, Keys: c.Keys}
	}
	if c.Exports != nil {
		if c.Exports.Store == nil || c.Exports.Presigner == nil {
			return nil, errors.New("query: exports need a store and a way to sign links to it")
		}
		s.Exporter = &inner.Exporter{
			Store: c.Exports.Store, Presigner: c.Exports.Presigner,
			Expiry: c.Exports.Expiry, LinkValid: c.Exports.LinkValid, MaxRecords: c.Exports.MaxRecords,
		}
	}
	started, err := inner.New(s)
	if err != nil {
		return nil, err
	}
	return &Service{inner: started, authenticator: c.Authenticator}, nil
}

// Handler is the Connect service, with snake_case JSON, at the path it must be
// mounted on. Every call is authenticated before anything is read.
func (s *Service) Handler() (string, http.Handler) {
	return inner.NewHandler(s.inner, s.authenticator)
}

// Close drains the service's own records into the sink.
func (s *Service) Close() error { return s.inner.Close() }
