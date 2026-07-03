// Package oidcauth validates bearer tokens issued by any OpenID Connect
// identity provider (Keycloak, Auth0, Entra ID, ...) against a configured
// issuer and audience. It is deliberately provider-agnostic: everything it
// needs comes from standard OIDC discovery and JWKS.
package oidcauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/andyjmorgan/obsidian-hosted-mcp/internal/config"
)

// Verifier validates OIDC bearer tokens for one issuer/audience pair.
type Verifier struct {
	audience string
	verifier *oidc.IDTokenVerifier
}

// New builds a Verifier by fetching the provider's discovery document from
// cfg.InternalIssuer (which defaults to the public issuer) and its JWKS.
// The discovery document's issuer must match cfg.Issuer — this is what lets
// a cluster-internal fetch URL coexist with public-issuer token validation.
// httpClient may be nil for http.DefaultClient.
func New(ctx context.Context, cfg *config.OAuth, httpClient *http.Client) (*Verifier, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	fetchBase := cfg.InternalIssuer
	if fetchBase == "" {
		fetchBase = cfg.Issuer
	}
	wellKnown := strings.TrimSuffix(fetchBase, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wellKnown, nil)
	if err != nil {
		return nil, fmt.Errorf("building OIDC discovery request: %w", err)
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching OIDC discovery document from %s: %w", wellKnown, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching OIDC discovery document from %s: HTTP %d", wellKnown, res.StatusCode)
	}
	var doc struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("parsing OIDC discovery document from %s: %w", wellKnown, err)
	}
	if doc.Issuer != cfg.Issuer {
		return nil, fmt.Errorf("OIDC discovery issuer %q does not match configured OAUTH_ISSUER %q", doc.Issuer, cfg.Issuer)
	}
	if doc.JWKSURI == "" {
		return nil, fmt.Errorf("OIDC discovery document from %s has no jwks_uri", wellKnown)
	}
	keySet := oidc.NewRemoteKeySet(oidc.ClientContext(ctx, httpClient), doc.JWKSURI)
	return &Verifier{
		audience: cfg.Audience,
		// Audience is checked by Verify below (aud with azp fallback), so
		// skip go-oidc's strict single-client check.
		verifier: oidc.NewVerifier(cfg.Issuer, keySet, &oidc.Config{SkipClientIDCheck: true}),
	}, nil
}

// Verify checks the raw bearer token's signature, issuer, lifetime, and
// audience, and maps its claims to MCP token info. The audience must appear
// in aud, or in azp — the authorized-party fallback some providers (notably
// Keycloak) use for client access tokens.
func (v *Verifier) Verify(ctx context.Context, token string) (*auth.TokenInfo, error) {
	idToken, err := v.verifier.Verify(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
	}
	var claims struct {
		Azp   string `json:"azp"`
		Scope string `json:"scope"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("%w: parsing claims: %v", auth.ErrInvalidToken, err)
	}
	if !slices.Contains(idToken.Audience, v.audience) && claims.Azp != v.audience {
		return nil, fmt.Errorf("%w: token audience %v (azp %q) does not include %q",
			auth.ErrInvalidToken, idToken.Audience, claims.Azp, v.audience)
	}
	return &auth.TokenInfo{
		UserID:     idToken.Subject,
		Scopes:     strings.Fields(claims.Scope),
		Expiration: idToken.Expiry,
	}, nil
}
