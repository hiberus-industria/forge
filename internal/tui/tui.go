// Package tui is forge's terminal UI (Bubble Tea v2).
package tui

import (
	"context"
	"errors"

	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/config"
	"github.com/hiberus-industria/forge/internal/daemon/localapi"
	"github.com/hiberus-industria/forge/internal/devflow"
)

// Deps are the TUI collaborators.
type Deps struct {
	Config  config.Config
	Version string
	Daemon  *localapi.Client
	API     *api.ClientWithResponses
	Flow    devflow.Service
}

// Run starts the TUI and blocks until the user quits.
func Run(ctx context.Context, d Deps) error {
	return errors.New("tui: not implemented yet")
}
