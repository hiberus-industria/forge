// Package daemon is the per-machine forge agent: it receives harness
// telemetry on a loopback listener, attributes every request to
// repo/branch/issue, buffers events in SQLite, syncs them to the API and
// serves the local API (localapi) to the TUI and CLI over a unix socket.
package daemon

import (
	"context"
	"errors"
	"log/slog"

	"github.com/hiberus-industria/forge/internal/auth"
	"github.com/hiberus-industria/forge/internal/config"
	"github.com/hiberus-industria/forge/internal/harness"
)

// Options wires the daemon.
type Options struct {
	Config  config.Config
	Version string
	Session auth.Session
	// Integrations are used for capture health in the status.
	Integrations []harness.Integration
	Logger       *slog.Logger
}

// Run blocks until ctx is cancelled.
func Run(ctx context.Context, opts Options) error {
	return errors.New("daemon: not implemented yet")
}
