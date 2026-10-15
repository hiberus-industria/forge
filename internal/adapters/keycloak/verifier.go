package keycloak

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/hiberus-industria/forge/internal/app"
)

// DefaultAudience is the client id of the forge API in Keycloak. Access
// tokens must carry it in "aud" (audience mapper on the forge-cli client).
const DefaultAudience = "forge-api"

// Verifier validates Keycloak access tokens against the realm JWKS.
type Verifier struct {
	verifier *oidc.IDTokenVerifier
	audience string
}

var _ app.TokenVerifier = (*Verifier)(nil)

// NewVerifier discovers the realm (issuer URL) and returns a verifier
// checking signature, issuer, expiry and audience.
func NewVerifier(ctx context.Context, issuer, audience string) (*Verifier, error) {
	if audience == "" {
		audience = DefaultAudience
	}
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery %s: %w", issuer, err)
	}
	return newVerifier(provider.VerifierContext(context.WithoutCancel(ctx), &oidc.Config{ClientID: audience}), audience), nil
}

func newVerifier(v *oidc.IDTokenVerifier, audience string) *Verifier {
	return &Verifier{verifier: v, audience: audience}
}

type accessClaims struct {
	Email             string `json:"email"`
	Name              string `json:"name"`
	PreferredUsername string `json:"preferred_username"`
	ResourceAccess    map[string]struct {
		Roles []string `json:"roles"`
	} `json:"resource_access"`
}

// Verify implements app.TokenVerifier.
func (v *Verifier) Verify(ctx context.Context, raw string) (app.Principal, error) {
	tok, err := v.verifier.Verify(ctx, raw)
	if err != nil {
		return app.Principal{}, fmt.Errorf("%w: %w", app.ErrUnauthorized, err)
	}
	var c accessClaims
	if err := tok.Claims(&c); err != nil {
		return app.Principal{}, fmt.Errorf("%w: %w", app.ErrUnauthorized, err)
	}
	name := strings.TrimSpace(c.Name)
	if name == "" {
		name = c.PreferredUsername
	}
	return app.Principal{
		Subject: tok.Subject,
		Email:   c.Email,
		Name:    name,
		Admin:   slices.Contains(c.ResourceAccess[v.audience].Roles, "admin"),
	}, nil
}
