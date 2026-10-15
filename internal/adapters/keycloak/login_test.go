package keycloak

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/hiberus-industria/forge/internal/adapters/credstore"
	"github.com/hiberus-industria/forge/internal/auth"
	"github.com/hiberus-industria/forge/internal/config"
)

// fakeIdP is a minimal OIDC provider: discovery, JWKS, authorize (auto
// approves) and token endpoint with refresh token rotation.
type fakeIdP struct {
	srv       *httptest.Server
	key       *rsa.PrivateKey
	nonces    map[string]string // code -> nonce
	refreshes atomic.Int32
	revoked   bool
}

func newFakeIdP(t *testing.T) *fakeIdP {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	f := &fakeIdP{key: k, nonces: map[string]string{}}
	mux := http.NewServeMux()
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	iss := f.srv.URL
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": iss, "authorization_endpoint": iss + "/auth", "token_endpoint": iss + "/token",
			"jwks_uri": iss + "/jwks", "device_authorization_endpoint": iss + "/device",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &k.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
			http.Error(w, "pkce required", 400)
			return
		}
		f.nonces["code1"] = q.Get("nonce")
		u, _ := url.Parse(q.Get("redirect_uri"))
		u.RawQuery = url.Values{"code": {"code1"}, "state": {q.Get("state")}}.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			if r.Form.Get("code_verifier") == "" {
				http.Error(w, `{"error":"invalid_grant"}`, 400)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"access_token": "at0", "refresh_token": "rt0", "token_type": "Bearer", "expires_in": 300,
				"id_token": f.idToken(t, f.nonces[r.Form.Get("code")]),
			})
		case "refresh_token":
			if f.revoked {
				w.WriteHeader(400)
				w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			n := f.refreshes.Add(1)
			json.NewEncoder(w).Encode(map[string]any{
				"access_token": "at" + string(rune('0'+n)), "refresh_token": "rt" + string(rune('0'+n)),
				"token_type": "Bearer", "expires_in": 300,
			})
		}
	})
	return f
}

func (f *fakeIdP) idToken(t *testing.T, nonce string) string {
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: f.key}, (&jose.SignerOptions{}).WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{
		"iss": f.srv.URL, "sub": "u-1", "aud": "forge-cli", "exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(), "nonce": nonce, "email": "dev@hiberus.com", "name": "Dev",
	}
	s, err := jwt.Signed(sig).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *fakeIdP) cfg() config.OIDC {
	return config.OIDC{Issuer: f.srv.URL, ClientID: "forge-cli", Scopes: []string{"openid", "offline_access"}}
}

func TestLoginBrowserPKCE(t *testing.T) {
	idp := newFakeIdP(t)
	ctx := context.Background()
	lc, err := NewLoginClient(ctx, idp.cfg())
	if err != nil {
		t.Fatal(err)
	}
	// The "browser" follows the redirect chain to the loopback callback.
	open := func(u string) error {
		go func() {
			resp, err := http.Get(u)
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
	res, err := lc.LoginBrowser(ctx, open, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Identity.Subject != "u-1" || res.Identity.Email != "dev@hiberus.com" || res.Token.RefreshToken != "rt0" {
		t.Fatalf("unexpected %+v", res)
	}
}

func TestSessionRefreshRotationAndRevocation(t *testing.T) {
	idp := newFakeIdP(t)
	store := credstore.New(filepath.Join(t.TempDir(), "creds.json"))
	sess := NewSession(idp.cfg(), store)
	ctx := context.Background()
	if err := sess.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Token(ctx); !errors.Is(err, auth.ErrNotLoggedIn) {
		t.Fatalf("want not logged in, got %v", err)
	}
	if _, err := store.Save(credstore.Credentials{RefreshToken: "rt0", Identity: auth.Identity{Name: "Dev"}}); err != nil {
		t.Fatal(err)
	}
	sess.Reload(ctx)
	tok, err := sess.Token(ctx)
	if err != nil || tok.AccessToken != "at1" {
		t.Fatalf("token %+v %v", tok, err)
	}
	if again, _ := sess.Token(ctx); again.AccessToken != "at1" || idp.refreshes.Load() != 1 {
		t.Fatal("cached token not reused")
	}
	c, _, _ := store.Load()
	if c.RefreshToken != "rt1" {
		t.Fatalf("rotated refresh token not persisted: %q", c.RefreshToken)
	}
	idp.revoked = true
	sess.Reload(ctx)
	if _, err := sess.Token(ctx); !errors.Is(err, auth.ErrNotLoggedIn) {
		t.Fatalf("revoked session: %v", err)
	}
	if _, ok := sess.Identity(); ok {
		t.Fatal("identity should be cleared")
	}
}
