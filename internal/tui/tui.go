// Package tui is forge's terminal UI (Bubble Tea v2).
//
// Four tabs: Tareas (lifecycle + cost per issue), En vivo (live feed from the
// daemon), Bandeja (triage of unassigned spend) and Informes (cost reports
// with CSV export). Set FORGE_TUI_DEMO=1 to run it against fake data, without
// daemon or API.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/config"
	"github.com/hiberus-industria/forge/internal/daemon/localapi"
	"github.com/hiberus-industria/forge/internal/devflow"
	"github.com/hiberus-industria/forge/internal/domain"
	"github.com/hiberus-industria/forge/internal/tui/theme"
)

// Deps are the TUI collaborators.
type Deps struct {
	Config  config.Config
	Version string
	Daemon  *localapi.Client
	API     *api.ClientWithResponses
	Flow    devflow.Service
}

// DemoEnv enables the demo mode with fake data.
const DemoEnv = "FORGE_TUI_DEMO"

// Run starts the TUI and blocks until the user quits.
func Run(ctx context.Context, d Deps) error {
	th, err := theme.Load(d.Config.ThemeFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "forge:", err, "(usando el tema por defecto)")
	}

	var be Backend
	if os.Getenv(DemoEnv) == "1" {
		be = newDemoBackend(time.Now())
	} else {
		if d.Daemon == nil || d.API == nil {
			return errors.New("tui: daemon and API clients are required")
		}
		be = liveBackend{d: d.Daemon, c: d.API, flow: d.Flow}
	}

	cur := domain.Currency(d.Config.Currency)
	if cur != domain.CurrencyUSD {
		cur = domain.CurrencyEUR
	}
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	org := d.Config.Org
	if org == "" {
		org = config.DefaultOrg
	}
	sh := newShared(ctx, be, th, org, cur, wd)
	_, err = tea.NewProgram(newModel(sh, d.Version), tea.WithContext(ctx)).Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil
	}
	return err
}
