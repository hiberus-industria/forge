package keycloak

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/hiberus-industria/forge/internal/auth"
	"github.com/hiberus-industria/forge/internal/config"
)

// LoginClient runs the interactive login of the forge CLI, a Keycloak
// public client: Authorization Code + PKCE with a loopback redirect
// (RFC 8252), or the Device Authorization Grant (RFC 8628) when no browser
// can be opened on this machine.
type LoginClient struct {
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// LoginResult is the outcome of a successful login.
type LoginResult struct {
	Token    *oauth2.Token
	Identity auth.Identity
}

// NewLoginClient discovers the issuer configuration.
func NewLoginClient(ctx context.Context, cfg config.OIDC) (*LoginClient, error) {
	oc, v, err := discover(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &LoginClient{oauth: oc, verifier: v}, nil
}

func discover(ctx context.Context, cfg config.OIDC) (oauth2.Config, *oidc.IDTokenVerifier, error) {
	p, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return oauth2.Config{}, nil, fmt.Errorf("discover %s: %w", cfg.Issuer, err)
	}
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "profile", "email", oidc.ScopeOfflineAccess}
	}
	oc := oauth2.Config{ClientID: cfg.ClientID, Endpoint: p.Endpoint(), Scopes: scopes}
	return oc, p.Verifier(&oidc.Config{ClientID: cfg.ClientID}), nil
}

// LoginBrowser runs Authorization Code + PKCE. open launches the browser;
// show is always called with the URL so the user can copy it manually.
func (c *LoginClient) LoginBrowser(ctx context.Context, open func(string) error, show func(string)) (LoginResult, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return LoginResult{}, fmt.Errorf("loopback listener: %w", err)
	}
	defer ln.Close()

	oc := c.oauth
	oc.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)
	state, nonce, verifier := randomString(), randomString(), oauth2.GenerateVerifier()
	authURL := oc.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))

	type outcome struct {
		res LoginResult
		err error
	}
	done := make(chan outcome, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		res, err := func() (LoginResult, error) {
			if e := q.Get("error"); e != "" {
				return LoginResult{}, fmt.Errorf("authorization denied: %s %s", e, q.Get("error_description"))
			}
			if q.Get("state") != state {
				return LoginResult{}, errors.New("state mismatch: possible CSRF, try again")
			}
			tok, err := oc.Exchange(r.Context(), q.Get("code"), oauth2.VerifierOption(verifier))
			if err != nil {
				return LoginResult{}, fmt.Errorf("exchange code: %w", err)
			}
			id, err := c.identity(r.Context(), tok, nonce)
			return LoginResult{Token: tok, Identity: id}, err
		}()
		renderCallback(w, res, err)
		select {
		case done <- outcome{res, err}:
		default:
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	show(authURL)
	if open != nil {
		_ = open(authURL) // the URL was shown; a failed launch is not fatal
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	select {
	case o := <-done:
		return o.res, o.err
	case <-ctx.Done():
		return LoginResult{}, fmt.Errorf("login timed out waiting for the browser: %w", ctx.Err())
	}
}

// DevicePrompt tells the user where to authorize the device.
type DevicePrompt struct {
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresAt               time.Time
}

// LoginDevice runs the Device Authorization Grant.
func (c *LoginClient) LoginDevice(ctx context.Context, prompt func(DevicePrompt)) (LoginResult, error) {
	if c.oauth.Endpoint.DeviceAuthURL == "" {
		return LoginResult{}, errors.New("the identity provider does not advertise a device authorization endpoint; enable 'OAuth 2.0 Device Authorization Grant' on the client")
	}
	// Keycloak enforces PKCE on the device endpoint too when the client
	// requires it, so send the challenge here and the verifier when polling.
	verifier := oauth2.GenerateVerifier()
	da, err := c.oauth.DeviceAuth(ctx, oauth2.S256ChallengeOption(verifier))
	if err != nil {
		return LoginResult{}, fmt.Errorf("device authorization: %w", err)
	}
	prompt(DevicePrompt{
		UserCode: da.UserCode, VerificationURI: da.VerificationURI,
		VerificationURIComplete: da.VerificationURIComplete, ExpiresAt: da.Expiry,
	})
	tok, err := c.oauth.DeviceAccessToken(ctx, da, oauth2.VerifierOption(verifier))
	if err != nil {
		return LoginResult{}, fmt.Errorf("device login: %w", err)
	}
	id, err := c.identity(ctx, tok, "")
	return LoginResult{Token: tok, Identity: id}, err
}

func (c *LoginClient) identity(ctx context.Context, tok *oauth2.Token, nonce string) (auth.Identity, error) {
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		return auth.Identity{}, errors.New("no id_token in token response (is the openid scope allowed?)")
	}
	idt, err := c.verifier.Verify(ctx, raw)
	if err != nil {
		return auth.Identity{}, fmt.Errorf("verify id token: %w", err)
	}
	if nonce != "" && idt.Nonce != nonce {
		return auth.Identity{}, errors.New("id token nonce mismatch")
	}
	var claims struct {
		Email             string `json:"email"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
	}
	if err := idt.Claims(&claims); err != nil {
		return auth.Identity{}, err
	}
	name := claims.Name
	if name == "" {
		name = claims.PreferredUsername
	}
	return auth.Identity{Subject: idt.Subject, Email: claims.Email, Name: name}, nil
}

func randomString() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

var callbackPage = template.Must(template.New("cb").Parse(`<!doctype html>
<html lang="es"><head><meta charset="utf-8"><title>forge</title>
<style>
:root{color-scheme:light dark;--bg:#f6f5fb;--fg:#1f1d2b;--card:#fff;--accent:#7c5cff;--muted:#6b6880;--err:#e5484d}
@media (prefers-color-scheme:dark){:root{--bg:#121118;--fg:#ecebf3;--card:#1c1a26;--muted:#9a97ad}}
body{margin:0;min-height:100vh;display:grid;place-items:center;background:var(--bg);color:var(--fg);font:16px/1.5 ui-sans-serif,system-ui,sans-serif}
.card{background:var(--card);padding:40px 48px;border-radius:16px;box-shadow:0 10px 40px #0002;text-align:center;max-width:420px}
.logo{font:700 28px ui-monospace,monospace;letter-spacing:.08em;background:linear-gradient(90deg,var(--accent),#ff6ec7);-webkit-background-clip:text;background-clip:text;color:transparent}
h1{font-size:20px;margin:16px 0 8px}p{color:var(--muted);margin:0}.err h1{color:var(--err)}
</style></head><body><div class="card {{if .Err}}err{{end}}">
<div class="logo">⚒ forge</div>
{{if .Err}}<h1>No se pudo iniciar sesión</h1><p>{{.Err}}</p>
{{else}}<h1>¡Hola{{with .Name}}, {{.}}{{end}}!</h1><p>Sesión iniciada. Ya puedes cerrar esta pestaña y volver a la terminal.</p>{{end}}
</div></body></html>`))

func renderCallback(w http.ResponseWriter, res LoginResult, err error) {
	data := struct{ Name, Err string }{Name: res.Identity.Name}
	if err != nil {
		data.Err = err.Error()
		w.WriteHeader(http.StatusBadRequest)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = callbackPage.Execute(w, data)
}
