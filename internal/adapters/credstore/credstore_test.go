package credstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hiberus-industria/forge/internal/auth"
)

type brokenKeyring struct{}

func (brokenKeyring) Set(string, string, string) error   { return errors.New("no keyring") }
func (brokenKeyring) Get(string, string) (string, error) { return "", errors.New("no keyring") }
func (brokenKeyring) Delete(string, string) error        { return errors.New("no keyring") }

type memKeyring map[string]string

func (m memKeyring) Set(s, u, p string) error { m[s+u] = p; return nil }
func (m memKeyring) Get(s, u string) (string, error) {
	v, ok := m[s+u]
	if !ok {
		return "", errors.New("not found")
	}
	return v, nil
}
func (m memKeyring) Delete(s, u string) error { delete(m, s+u); return nil }

func TestFileFallback(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state", "credentials.json")
	s := &Store{file: file, keyring: brokenKeyring{}}
	if _, _, err := s.Load(); !errors.Is(err, auth.ErrNotLoggedIn) {
		t.Fatalf("want ErrNotLoggedIn, got %v", err)
	}
	be, err := s.Save(Credentials{RefreshToken: "rt", Identity: auth.Identity{Email: "a@b"}})
	if err != nil || be != BackendFile {
		t.Fatalf("save: %v %v", be, err)
	}
	st, _ := os.Stat(file)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	c, _, err := s.Load()
	if err != nil || c.RefreshToken != "rt" || c.Identity.Email != "a@b" {
		t.Fatalf("load: %+v %v", c, err)
	}
	if err := s.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Load(); !errors.Is(err, auth.ErrNotLoggedIn) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestKeyringPreferred(t *testing.T) {
	file := filepath.Join(t.TempDir(), "credentials.json")
	os.WriteFile(file, []byte(`{"refresh_token":"old"}`), 0o600)
	s := &Store{file: file, keyring: memKeyring{}}
	if be, err := s.Save(Credentials{RefreshToken: "new"}); err != nil || be != BackendKeyring {
		t.Fatalf("%v %v", be, err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("stale file not removed")
	}
	c, be, err := s.Load()
	if err != nil || be != BackendKeyring || c.RefreshToken != "new" {
		t.Fatalf("%+v %v %v", c, be, err)
	}
}
