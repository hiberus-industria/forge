package cli

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/hiberus-industria/forge/internal/adapters/claudecode"
	"github.com/hiberus-industria/forge/internal/adapters/keycloak"
	"github.com/hiberus-industria/forge/internal/adapters/opencode"
	"github.com/hiberus-industria/forge/internal/daemon"
	"github.com/hiberus-industria/forge/internal/harness"
	"github.com/hiberus-industria/forge/internal/platform/service"
)

func integrations() []harness.Integration {
	return []harness.Integration{claudecode.New(), opencode.New()}
}

func daemonCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "daemon",
		Short: "Gestiona el daemon que captura el uso de los harnesses",
	}
	c.AddCommand(daemonRunCmd(), daemonInstallCmd(), daemonUninstallCmd(), daemonStatusCmd(), daemonLogsCmd())
	return c
}

func daemonRunCmd() *cobra.Command {
	var debug bool
	c := &cobra.Command{
		Use:   "run",
		Short: "Ejecuta el daemon en primer plano",
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := load(cmd)
			if err != nil {
				return err
			}
			level := slog.LevelInfo
			if debug {
				level = slog.LevelDebug
			}
			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
			sess := keycloak.NewSession(e.cfg.OIDC, e.store)
			if err := sess.Reload(cmd.Context()); err != nil {
				logger.Warn("cannot load credentials", "err", err)
			}
			return daemon.Run(cmd.Context(), daemon.Options{
				Config:       e.cfg,
				Version:      version,
				Session:      sess,
				Integrations: integrations(),
				Logger:       logger,
			})
		},
	}
	c.Flags().BoolVar(&debug, "debug", false, "logs de depuración")
	return c
}

func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

func daemonInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Instala el daemon como servicio de usuario (systemd/launchd)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			o := out{w: cmd.OutOrStdout()}
			return installService(o)
		},
	}
}

func installService(o out) error {
	exe, err := executable()
	if err != nil {
		return err
	}
	st, err := service.Install(exe)
	if errors.Is(err, service.ErrUnsupported) {
		o.warn("No hay gestor de servicios de usuario disponible.")
		o.println(sMuted.Render(service.Fallback))
		return nil
	}
	if err != nil {
		return err
	}
	o.ok("Daemon instalado con %s (%s)", st.Manager, st.Detail)
	o.hint("%s", st.Path)
	return nil
}

func daemonUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Detiene y elimina el servicio del daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			o := out{w: cmd.OutOrStdout()}
			if err := service.Uninstall(); err != nil {
				return err
			}
			o.ok("Servicio eliminado")
			return nil
		},
	}
}

func daemonStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Estado del servicio del daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			o := out{w: cmd.OutOrStdout()}
			st, err := service.Get()
			if err != nil {
				return err
			}
			o.title("Servicio del daemon")
			o.kv("gestor", st.Manager)
			o.kv("instalado", yesNo(st.Installed))
			o.kv("estado", stateColor(st.Running, st.Detail))
			if st.Path != "" {
				o.kv("fichero", sMuted.Render(st.Path))
			}
			return nil
		},
	}
}

func daemonLogsCmd() *cobra.Command {
	var follow bool
	c := &cobra.Command{
		Use:   "logs",
		Short: "Muestra los logs del daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			lc := service.LogsCommand(follow)
			lc.Stdout, lc.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
			return lc.Run()
		},
	}
	c.Flags().BoolVarP(&follow, "follow", "f", false, "seguir los logs")
	return c
}

func yesNo(b bool) string {
	if b {
		return sOK.Render("sí")
	}
	return sWarn.Render("no")
}

func stateColor(ok bool, s string) string {
	if s == "" {
		s = fmt.Sprint(ok)
	}
	if ok {
		return sOK.Render("● " + s)
	}
	return sErr.Render("○ " + s)
}
