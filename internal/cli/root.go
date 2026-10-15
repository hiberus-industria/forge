// Package cli implements the `forge` command line: login, daemon, setup,
// doctor, lifecycle commands and the TUI entry point.
package cli

import (
	"context"
	"errors"
	"image/color"
	"os"

	"charm.land/fang/v2"
	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/hiberus-industria/forge/internal/adapters/credstore"
	"github.com/hiberus-industria/forge/internal/adapters/keycloak"
	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/apiclient"
	"github.com/hiberus-industria/forge/internal/auth"
	"github.com/hiberus-industria/forge/internal/config"
	"github.com/hiberus-industria/forge/internal/daemon/localapi"
)

// Set at build time with -ldflags -X.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// Version returns the build version.
func Version() string { return version }

// BuildInfo describes the build for bug reports.
func BuildInfo() string { return version + " (" + commit + ", " + date + ")" }

// Execute runs the CLI.
func Execute(ctx context.Context) error {
	return fang.Execute(ctx, newRoot(),
		fang.WithVersion(version),
		fang.WithCommit(commit),
		fang.WithColorSchemeFunc(colorScheme),
		fang.WithNotifySignal(os.Interrupt),
	)
}

func colorScheme(c lipgloss.LightDarkFunc) fang.ColorScheme {
	cs := fang.DefaultColorScheme(c)
	cs.Title = c(lipgloss.Color("#5B3FD9"), lipgloss.Color("#9D85FF"))
	cs.Program = c(lipgloss.Color("#5B3FD9"), lipgloss.Color("#9D85FF"))
	cs.Command = c(lipgloss.Color("#C2378F"), lipgloss.Color("#FF7AC6"))
	cs.Flag = c(lipgloss.Color("#1A8F55"), lipgloss.Color("#3DD68C"))
	cs.ErrorHeader = [2]color.Color{lipgloss.Color("#FFFFFF"), lipgloss.Color("#E5484D")}
	return cs
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "forge",
		Short: "Trazabilidad del gasto IA por repositorio e issue",
		Long: `forge relaciona el gasto de Claude Code y OpenCode con el trabajo en GitHub.

Sin argumentos abre la TUI. El ciclo de vida es: forge start → trabajo → forge pr → merge.`,
		Example: `  forge login
  forge setup
  forge start 123
  forge pr --draft`,
		RunE: func(cmd *cobra.Command, _ []string) error { return runTUI(cmd.Context()) },
	}
	root.AddGroup(
		&cobra.Group{ID: "flow", Title: "Ciclo de vida"},
		&cobra.Group{ID: "setup", Title: "Configuración"},
		&cobra.Group{ID: "info", Title: "Información"},
	)
	add := func(group string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = group
			root.AddCommand(c)
		}
	}
	add("flow", startCmd(), prCmd(), taskCmd(), tuiCmd())
	add("setup", loginCmd(), logoutCmd(), setupCmd(), daemonCmd(), configCmd(), updateCmd())
	add("info", whoamiCmd(), statusCmd(), syncCmd(), doctorCmd())
	return root
}

// env bundles what most commands need.
type env struct {
	cfg    config.Config
	daemon *localapi.Client
	store  *credstore.Store
	o      out
}

func load(cmd *cobra.Command) (*env, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return &env{
		cfg:    cfg,
		daemon: localapi.NewClient(config.SocketPath()),
		store:  credstoreFor(),
		o:      out{w: cmd.OutOrStdout()},
	}, nil
}

// tokens asks the daemon for an access token and, when it is not running,
// refreshes in-process from the stored credentials.
func (e *env) tokens() auth.TokenSource {
	return fallbackTokens{daemon: e.daemon, session: keycloak.NewSession(e.cfg.OIDC, e.store)}
}

type fallbackTokens struct {
	daemon  *localapi.Client
	session *keycloak.Session
}

func (f fallbackTokens) Token(ctx context.Context) (auth.Token, error) {
	t, err := f.daemon.Token(ctx)
	if !errors.Is(err, localapi.ErrDaemonDown) {
		return t, err
	}
	if _, ok := f.session.Identity(); !ok {
		if err := f.session.Reload(ctx); err != nil {
			return auth.Token{}, err
		}
	}
	return f.session.Token(ctx)
}

func (e *env) api() (*api.ClientWithResponses, error) {
	return apiclient.New(e.cfg.APIURL, e.tokens(), "forge/"+version)
}

// apiError turns a non-2xx generated response into an error.
func apiError(status int, e *api.Error) error {
	if status < 300 {
		return nil
	}
	if e != nil {
		return errors.New(e.Message)
	}
	return errors.New(httpStatus(status))
}

func credstoreFor() *credstore.Store { return credstore.New(config.CredentialsFile()) }
