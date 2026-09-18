package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	gatewayauth "github.com/truvity/gateway-auth"
)

// ErrUnauthenticated is what a caller is told when its credential is missing or
// does not verify. It says nothing more on purpose: the reason goes to the log,
// because telling an unauthenticated client why its token failed helps it
// forge a better one.
var ErrUnauthenticated = errors.New("auth: unauthenticated")

// Issuer is a token issuer a deployment trusts.
type Issuer struct {
	// URL is the issuer exactly as its tokens name it. Discovery and the key set
	// are found from it, and a token is sent to this issuer's keys only if its
	// iss claim is this string, byte for byte.
	URL string
	// Audience is required. An audit log is the last API that should accept a
	// token minted for some other service: a workload that received a user's
	// token could otherwise replay it here and read the trail under that user's
	// grants.
	Audience string
}

// JWT authenticates bearer tokens from any of several issuers.
//
// Verification is gateway-auth's, one verifier per issuer, so that a token is
// checked here exactly as every other service in the fleet checks it: key set
// fetched by discovery and refreshed in the background, signature, issuer,
// audience and expiry. What this adds is the choice between issuers. A token's
// iss claim is read before verification only to pick whose keys to check it
// with, and the chosen verifier then requires that same issuer, so a token
// claiming the wrong issuer fails verification rather than being believed.
//
// Several issuers make one thing possible that a single issuer does not: two
// of them asserting the same claim. See Rule.Issuer.
type JWT struct {
	verifiers map[string]gatewayauth.Authenticator
	source    gatewayauth.TokenSource
	logger    *slog.Logger
}

// NewJWT builds the authenticator, discovering every issuer now so that one
// that is unreachable or misnamed stops the service at start-up rather than on
// its first request.
func NewJWT(ctx context.Context, issuers []Issuer, logger *slog.Logger) (*JWT, error) {
	if len(issuers) == 0 {
		return nil, errors.New("auth: a JWT authenticator needs at least one issuer")
	}
	if logger == nil {
		logger = slog.Default()
	}
	j := &JWT{
		verifiers: make(map[string]gatewayauth.Authenticator, len(issuers)),
		// The gateway's forwarded token first, then a bearer: both are
		// verified, so the order only decides which is read when both exist.
		source: gatewayauth.DefaultSource(),
		logger: logger,
	}
	for _, is := range issuers {
		if is.URL == "" {
			return nil, errors.New("auth: an issuer needs a URL")
		}
		if is.Audience == "" {
			return nil, fmt.Errorf("auth: issuer %s needs an audience; an audit log must not "+
				"accept a token minted for another service", is.URL)
		}
		if _, twice := j.verifiers[is.URL]; twice {
			return nil, fmt.Errorf("auth: issuer %s is listed twice", is.URL)
		}
		v, err := gatewayauth.NewVerifier(ctx, gatewayauth.Config{
			Issuer: is.URL, Audience: is.Audience, Source: j.source, Logger: logger,
		})
		if err != nil {
			return nil, fmt.Errorf("auth: issuer %s: %w", is.URL, err)
		}
		j.verifiers[is.URL] = v
	}
	return j, nil
}

// Principal implements Authenticator.
func (j *JWT) Principal(ctx context.Context, req *http.Request) (Principal, error) {
	headers := gatewayauth.HeaderGetter(req.Header.Get)
	raw, ok := j.source.Token(headers)
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	claimed, err := issuerOf(raw)
	if err != nil {
		j.logger.WarnContext(ctx, "token rejected", "error", err)
		return Principal{}, ErrUnauthenticated
	}
	verifier, ok := j.verifiers[claimed]
	if !ok {
		j.logger.WarnContext(ctx, "token rejected: not a trusted issuer", "issuer", claimed)
		return Principal{}, ErrUnauthenticated
	}
	id, err := verifier.Authenticate(ctx, headers)
	if err != nil {
		// gateway-auth has logged the reason already.
		return Principal{}, ErrUnauthenticated
	}
	if id.Subject == "" {
		j.logger.WarnContext(ctx, "token rejected: no subject", "issuer", claimed)
		return Principal{}, ErrUnauthenticated
	}
	return Principal{
		Issuer:  claimed,
		Subject: id.Subject,
		Claims:  flatten(id.Claims),
		Via:     "oidc",
	}, nil
}

// issuerOf reads a token's iss claim without verifying anything. It is used to
// choose which issuer's keys to verify with, and for nothing else.
func issuerOf(raw string) (string, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return "", errors.New("not a signed JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("payload: %w", err)
	}
	var claims struct {
		Issuer string `json:"iss"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("payload: %w", err)
	}
	if claims.Issuer == "" {
		return "", errors.New("no issuer")
	}
	return claims.Issuer, nil
}

// flatten turns a token's claims into the shape a rule matches: every value a
// list of strings. A provider may send one group as a string or as a
// one-element list, and a rule should not have to know which.
func flatten(claims map[string]any) map[string][]string {
	out := make(map[string][]string, len(claims))
	for name, value := range claims {
		switch v := value.(type) {
		case string:
			out[name] = []string{v}
		case []string:
			out[name] = append([]string(nil), v...)
		case []any:
			for _, item := range v {
				if s, ok := item.(string); ok {
					out[name] = append(out[name], s)
				}
			}
		case nil:
		default:
			// Numbers, booleans and objects are not what a rule names; they
			// are kept as text so a deployment can still see them, not so a
			// rule can match on them.
			out[name] = []string{fmt.Sprint(v)}
		}
	}
	return out
}
