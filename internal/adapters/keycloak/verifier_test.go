package keycloak

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"

	"github.com/hiberus-industria/forge/internal/app"
)

const testIssuer = "https://sso.example.com/realms/hiberus"

func signToken(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(claims)
	jws, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	s, err := jws.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestVerifier(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keySet := &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}
	v := newVerifier(oidc.NewVerifier(testIssuer, keySet, &oidc.Config{ClientID: DefaultAudience}), DefaultAudience)

	base := func() map[string]any {
		return map[string]any{
			"iss": testIssuer, "sub": "user-1", "aud": []string{"account", DefaultAudience},
			"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
			"email": "dev@example.com", "preferred_username": "dev",
			"resource_access": map[string]any{DefaultAudience: map[string]any{"roles": []string{"member", "admin"}}},
		}
	}

	p, err := v.Verify(context.Background(), signToken(t, key, base()))
	if err != nil {
		t.Fatal(err)
	}
	if p.Subject != "user-1" || p.Email != "dev@example.com" || p.Name != "dev" || !p.Admin {
		t.Fatalf("unexpected principal %+v", p)
	}

	bad := map[string]func(map[string]any){
		"wrong audience": func(c map[string]any) { c["aud"] = "account" },
		"expired":        func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() },
		"wrong issuer":   func(c map[string]any) { c["iss"] = "https://evil.example.com" },
	}
	for name, mutate := range bad {
		c := base()
		mutate(c)
		if _, err := v.Verify(context.Background(), signToken(t, key, c)); !errors.Is(err, app.ErrUnauthorized) {
			t.Errorf("%s: want ErrUnauthorized, got %v", name, err)
		}
	}

	c := base()
	c["resource_access"] = map[string]any{"other": map[string]any{"roles": []string{"admin"}}}
	if p, err := v.Verify(context.Background(), signToken(t, key, c)); err != nil || p.Admin {
		t.Fatalf("admin role of another client must not count: %+v %v", p, err)
	}
}
