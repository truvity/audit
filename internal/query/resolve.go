package query

import (
	"context"
	"errors"
	"fmt"

	"github.com/truvity/audit/auth"
	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/internal/identity"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/record"
)

// ErrErased is a pseudonym whose tenant key has been destroyed: the way back
// is gone on purpose, and saying so is the answer rather than a failure.
var ErrErased = errors.New("query: the key behind this pseudonym has been destroyed; it can no longer be resolved")

// Resolve maps a pseudonym back to the identity behind it.
//
// It is the one read that undoes the pseudonymisation, so it is fenced three
// ways. It needs the resolve operation on the profile, which no read grant
// implies and no group name grants (only an explicit rule naming a person).
// The tenant must be one the grant covers. And the resolution is recorded,
// and the record confirmed, before the identity is returned: if the trail
// cannot take it, nothing is resolved. The record names the pseudonym and the
// rule — never the identity it resolved to.
func (s *Service) Resolve(
	ctx context.Context, p auth.Principal, req *auditv1.ResolveRequest,
) (string, error) {
	if s.Identities == nil {
		return "", fmt.Errorf("%w: resolve needs the pseudonymisation keys, which this service was not given", ErrNotOffered)
	}
	if req.GetProfile() == "" || req.GetTenantId() == "" || req.GetPseudonym() == "" {
		return "", fmt.Errorf("%w: name the profile, the tenant and the pseudonym", ErrMalformed)
	}
	g, err := s.allow(ctx, p, req.GetProfile(), auth.Resolve)
	if err != nil {
		return "", err
	}
	if !granted(req.GetTenantId(), g) {
		// Another tenant's pseudonym reads as unknown, not as forbidden: the
		// two are the same answer to someone who should not know it exists.
		return "", fmt.Errorf("%w: no identity for this pseudonym", ErrNotFound)
	}

	r := s.readRecord("audit.pseudonym.resolved", p, g, nil, []*record.Target{
		{Type: "pseudonym", Id: req.GetPseudonym()},
		{Type: "tenant", Id: req.GetTenantId()},
		{Type: "profile", Id: req.GetProfile()},
	}, nil)
	if s.confirmed == nil {
		return "", fmt.Errorf("%w: resolving needs a writer to record it", ErrNotOffered)
	}
	if err := s.confirmed.Record(ctx, r); err != nil {
		return "", fmt.Errorf("query: the resolution could not be recorded, so it was not made: %w", err)
	}

	id, err := s.Identities.Resolve(ctx, req.GetTenantId(), keys.Purpose(req.GetProfile()), req.GetPseudonym())
	switch {
	case errors.Is(err, identity.ErrUnknown):
		return "", fmt.Errorf("%w: no identity for this pseudonym", ErrNotFound)
	case errors.Is(err, keys.ErrDestroyed):
		return "", ErrErased
	case err != nil:
		return "", err
	}
	return id, nil
}
