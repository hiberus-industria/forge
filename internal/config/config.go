// Package config loads the developer-side configuration shared by the CLI,
// daemon and TUI, and knows where forge keeps its files.
//
// Precedence: built-in defaults (overridable at build time with -ldflags, so
// the team's install.sh ships a ready-to-use binary) < config file
// (~/.config/forge/config.toml) < FORGE_* environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Build-time defaults, set with:
//
//	-ldflags "-X github.com/hiberus-industria/forge/internal/config.DefaultAPIURL=https://forge.example.com ..."
var (
	DefaultAPIURL       = "http://localhost:8080"
	DefaultOIDCIssuer   = "http://localhost:8180/realms/hiberus"
	DefaultOIDCClientID = "forge-cli"
	DefaultOrg          = "hiberus-industria"
)

// Config is the developer-side configuration.
type Config struct {
	// APIURL is the base URL of the central forge API.
	APIURL string `toml:"api_url"`
	// Org is the GitHub organization forge tracks.
	Org  string `toml:"org"`
	OIDC OIDC   `toml:"oidc"`
	// IngestAddr is the loopback address where the daemon receives harness
	// telemetry (OTLP/HTTP, Claude Code hooks, OpenCode plugin).
	IngestAddr string `toml:"ingest_addr"`
	// Currency is the preferred display currency (USD or EUR); both are shown
	// where space allows.
	Currency string `toml:"currency"`
	// ThemeFile optionally points to a TOML theme overriding the palette.
	ThemeFile string `toml:"theme_file"`
}

// OIDC configures the Keycloak public client used by the CLI.
type OIDC struct {
	Issuer   string   `toml:"issuer"`
	ClientID string   `toml:"client_id"`
	Scopes   []string `toml:"scopes"`
}

// Defaults returns the built-in configuration.
func Defaults() Config {
	return Config{
		APIURL: DefaultAPIURL,
		Org:    DefaultOrg,
		OIDC: OIDC{
			Issuer:   DefaultOIDCIssuer,
			ClientID: DefaultOIDCClientID,
			Scopes:   []string{"openid", "profile", "email", "offline_access"},
		},
		IngestAddr: "127.0.0.1:47318",
		Currency:   "EUR",
	}
}

// Load reads the config file (if any) over the defaults and applies env vars.
func Load() (Config, error) {
	cfg := Defaults()
	path := File()
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := toml.Unmarshal(b, &cfg); err != nil {
			return cfg, fmt.Errorf("parse %s: %w", path, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return cfg, err
	}
	env := map[string]*string{
		"FORGE_API_URL":        &cfg.APIURL,
		"FORGE_ORG":            &cfg.Org,
		"FORGE_OIDC_ISSUER":    &cfg.OIDC.Issuer,
		"FORGE_OIDC_CLIENT_ID": &cfg.OIDC.ClientID,
		"FORGE_INGEST_ADDR":    &cfg.IngestAddr,
		"FORGE_CURRENCY":       &cfg.Currency,
		"FORGE_THEME_FILE":     &cfg.ThemeFile,
	}
	for k, dst := range env {
		if v := os.Getenv(k); v != "" {
			*dst = v
		}
	}
	cfg.APIURL = strings.TrimRight(cfg.APIURL, "/")
	cfg.Currency = strings.ToUpper(cfg.Currency)
	return cfg, cfg.Validate()
}

// Validate checks required settings.
func (c Config) Validate() error {
	switch {
	case c.APIURL == "":
		return errors.New("config: api_url is required")
	case c.Org == "":
		return errors.New("config: org is required")
	case c.OIDC.Issuer == "" || c.OIDC.ClientID == "":
		return errors.New("config: oidc.issuer and oidc.client_id are required")
	case c.Currency != "USD" && c.Currency != "EUR":
		return fmt.Errorf("config: unsupported currency %q", c.Currency)
	}
	return nil
}

// Save writes the configuration file.
func Save(c Config) error {
	b, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(File(), b, 0o644)
}

// IngestURL is the daemon loopback base URL harnesses talk to.
func (c Config) IngestURL() string { return "http://" + c.IngestAddr }

// ---------------------------------------------------------------- paths

// Dir is the configuration directory (~/.config/forge, ~/Library/Application Support/forge).
func Dir() string {
	if d := os.Getenv("FORGE_CONFIG_DIR"); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = filepath.Join(home(), ".config")
	}
	return filepath.Join(base, "forge")
}

// File is the configuration file path.
func File() string { return filepath.Join(Dir(), "config.toml") }

// StateDir holds the daemon database, machine id and logs.
func StateDir() string {
	if d := os.Getenv("FORGE_STATE_DIR"); d != "" {
		return d
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home(), "Library", "Application Support", "forge", "state")
	}
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "forge")
	}
	return filepath.Join(home(), ".local", "state", "forge")
}

// DBPath is the daemon's SQLite buffer.
func DBPath() string { return filepath.Join(StateDir(), "daemon.db") }

// LogPath is the daemon log file (also used by launchd/systemd units).
func LogPath() string { return filepath.Join(StateDir(), "daemon.log") }

// CredentialsFile is the fallback credential store when no OS keyring is
// available (headless Linux, most WSL2 setups). Mode 0600.
func CredentialsFile() string { return filepath.Join(StateDir(), "credentials.json") }

// SocketPath is the daemon unix socket.
func SocketPath() string {
	if p := os.Getenv("FORGE_SOCKET"); p != "" {
		return p
	}
	if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" && runtime.GOOS == "linux" {
		return filepath.Join(x, "forge", "daemon.sock")
	}
	return filepath.Join(StateDir(), "daemon.sock")
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}
