// Package credstore persists the developer's Keycloak refresh token and
// identity. It prefers the OS keyring (macOS Keychain, Secret Service) and
// falls back to a 0600 file when no keyring is reachable (headless Linux,
// most WSL2 setups).
package credstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"

	"github.com/hiberus-industria/forge/internal/auth"
)

const (
	service = "forge"
	account = "credentials"
)

// Credentials is what forge keeps between runs.
type Credentials struct {
	RefreshToken string        `json:"refresh_token"`
	Identity     auth.Identity `json:"identity"`
}

// Backend names where credentials live, for `forge doctor`/`whoami`.
type Backend string

const (
	BackendKeyring Backend = "keyring"
	BackendFile    Backend = "file"
)

// Store reads and writes credentials.
type Store struct {
	file string
	// keyring is swappable in tests.
	keyring keyringAPI
}

type keyringAPI interface {
	Set(service, user, password string) error
	Get(service, user string) (string, error)
	Delete(service, user string) error
}

type osKeyring struct{}

func (osKeyring) Set(s, u, p string) error        { return keyring.Set(s, u, p) }
func (osKeyring) Get(s, u string) (string, error) { return keyring.Get(s, u) }
func (osKeyring) Delete(s, u string) error        { return keyring.Delete(s, u) }

// New returns a store using the OS keyring with file fallback at file.
func New(file string) *Store { return &Store{file: file, keyring: osKeyring{}} }

// Load returns stored credentials or auth.ErrNotLoggedIn.
func (s *Store) Load() (Credentials, Backend, error) {
	var c Credentials
	if raw, err := s.keyring.Get(service, account); err == nil {
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return c, BackendKeyring, fmt.Errorf("decode keyring credentials: %w", err)
		}
		return c, BackendKeyring, nil
	}
	b, err := os.ReadFile(s.file)
	if errors.Is(err, os.ErrNotExist) {
		return c, "", auth.ErrNotLoggedIn
	}
	if err != nil {
		return c, BackendFile, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, BackendFile, fmt.Errorf("decode %s: %w", s.file, err)
	}
	if c.RefreshToken == "" {
		return c, BackendFile, auth.ErrNotLoggedIn
	}
	return c, BackendFile, nil
}

// Save stores credentials, in the keyring when possible.
func (s *Store) Save(c Credentials) (Backend, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	if err := s.keyring.Set(service, account, string(b)); err == nil {
		// Do not leave a stale copy behind once the keyring works.
		_ = os.Remove(s.file)
		return BackendKeyring, nil
	}
	if err := os.MkdirAll(filepath.Dir(s.file), 0o700); err != nil {
		return "", err
	}
	tmp := s.file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return "", err
	}
	return BackendFile, os.Rename(tmp, s.file)
}

// Delete removes credentials from every backend.
func (s *Store) Delete() error {
	kerr := s.keyring.Delete(service, account)
	ferr := os.Remove(s.file)
	if errors.Is(kerr, keyring.ErrNotFound) {
		kerr = nil
	}
	if errors.Is(ferr, os.ErrNotExist) {
		ferr = nil
	}
	if ferr != nil {
		return ferr
	}
	// A missing/unavailable keyring is not a logout failure.
	_ = kerr
	return nil
}
