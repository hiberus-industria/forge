package keycloak

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/hiberus-industria/forge/internal/adapters/credstore"
	"github.com/hiberus-industria/forge/internal/auth"
	"github.com/hiberus-industria/forge/internal/config"
)

// Session implements auth.Session: it holds the refresh token persisted by
// `forge login`, refreshes access tokens and persists rotated refresh tokens.
// Discovery is lazy so the daemon can start while offline.
type Session struct {
	cfg   config.OIDC
	store *credstore.Store

	mu     sync.Mutex
	oauth  *oauth2.Config
	creds  *credstore.Credentials
	access *oauth2.Token
}

var _ auth.Session = (*Session)(nil)

// NewSession creates a session; call Reload to load stored credentials.
func NewSession(cfg config.OIDC, store *credstore.Store) *Session {
	return &Session{cfg: cfg, store: store}
}

// Reload re-reads stored credentials and drops any cached access token.
func (s *Session) Reload(context.Context) error {
	c, _, err := s.store.Load()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.access = nil
	if errors.Is(err, auth.ErrNotLoggedIn) {
		s.creds = nil
		return nil
	}
	if err != nil {
		s.creds = nil
		return err
	}
	s.creds = &c
	return nil
}

// Identity returns the logged-in developer.
func (s *Session) Identity() (auth.Identity, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.creds == nil {
		return auth.Identity{}, false
	}
	return s.creds.Identity, true
}

// Token returns a valid access token, refreshing it when it expires in
// less than 30 seconds.
func (s *Session) Token(ctx context.Context) (auth.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.creds == nil {
		return auth.Token{}, auth.ErrNotLoggedIn
	}
	if s.access != nil && time.Until(s.access.Expiry) > 30*time.Second {
		return auth.Token{AccessToken: s.access.AccessToken, ExpiresAt: s.access.Expiry}, nil
	}
	if s.oauth == nil {
		oc, _, err := discover(ctx, s.cfg)
		if err != nil {
			return auth.Token{}, err
		}
		s.oauth = &oc
	}
	t, err := s.oauth.TokenSource(ctx, &oauth2.Token{RefreshToken: s.creds.RefreshToken}).Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && re.ErrorCode == "invalid_grant" {
			// Session revoked or expired in Keycloak: force a new login.
			s.creds, s.access = nil, nil
			return auth.Token{}, fmt.Errorf("%w (session expired)", auth.ErrNotLoggedIn)
		}
		return auth.Token{}, fmt.Errorf("refresh token: %w", err)
	}
	if t.RefreshToken != "" && t.RefreshToken != s.creds.RefreshToken {
		s.creds.RefreshToken = t.RefreshToken
		if _, err := s.store.Save(*s.creds); err != nil {
			return auth.Token{}, fmt.Errorf("persist rotated refresh token: %w", err)
		}
	}
	s.access = t
	return auth.Token{AccessToken: t.AccessToken, ExpiresAt: t.Expiry}, nil
}

// Persist stores the outcome of a login.
func Persist(store *credstore.Store, res LoginResult) (credstore.Backend, error) {
	if res.Token.RefreshToken == "" {
		return "", errors.New("identity provider returned no refresh token (is offline_access allowed for the client?)")
	}
	return store.Save(credstore.Credentials{RefreshToken: res.Token.RefreshToken, Identity: res.Identity})
}
