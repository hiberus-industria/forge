// Package auth declares the developer-side identity contracts shared by the
// CLI, the daemon and the TUI. Keycloak specifics live in adapters/keycloak.
package auth

import (
	"context"
	"errors"
	"time"
)

// ErrNotLoggedIn means there are no stored credentials; run `forge login`.
var ErrNotLoggedIn = errors.New("not logged in: run `forge login`")

// TokenSource yields a valid access token for the forge API, refreshing it
// when needed. Only the daemon refreshes; other processes ask the daemon.
type TokenSource interface {
	Token(ctx context.Context) (Token, error)
}

// Token is an OAuth2 access token.
type Token struct {
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// Identity is what the CLI knows about the logged-in developer (ID token claims).
type Identity struct {
	Subject string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name"`
}

// Session is the daemon-side credential holder: it loads the refresh token
// from the credential store, refreshes access tokens against Keycloak and
// can be told to reload after `forge login`/`forge logout`.
type Session interface {
	TokenSource
	// Reload re-reads stored credentials. No credentials is not an error:
	// Token then returns ErrNotLoggedIn.
	Reload(ctx context.Context) error
	// Identity returns the logged-in developer, false when logged out.
	Identity() (Identity, bool)
}
