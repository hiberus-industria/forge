package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"github.com/hiberus-industria/forge/internal/config"
	"github.com/hiberus-industria/forge/internal/daemon/localapi"
	"github.com/hiberus-industria/forge/internal/devflow"
	"github.com/hiberus-industria/forge/internal/domain"
	"github.com/hiberus-industria/forge/internal/harness"
	"github.com/hiberus-industria/forge/internal/platform/service"
)

func setupCmd() *cobra.Command {
	var (
		yes, remove bool
		only        string
	)
	c := &cobra.Command{
		Use:   "setup",
		Short: "Conecta Claude Code y OpenCode con el daemon (muestra el diff antes de aplicar)",
		Long: `Configura cada harness para enviar telemetría de uso al daemon local:
  • Claude Code: variables OpenTelemetry y hooks HTTP en ~/.claude/settings.json
  • OpenCode: plugin global de forge

Es idempotente y reversible (--remove). Antes de modificar un fichero guarda una copia .forge-bak-<fecha>.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := load(cmd)
			if err != nil {
				return err
			}
			ep := harness.Endpoint{BaseURL: e.cfg.IngestURL()}
			selected := integrations()
			if only != "" {
				h := domain.Harness(only)
				if !h.Valid() {
					return fmt.Errorf("harness desconocido %q (claude-code u opencode)", only)
				}
				selected = filterHarness(selected, h)
			}
			e.o.title("forge setup")
			changed := false
			for _, in := range selected {
				name := sBold.Render(string(in.Harness()))
				if !in.Installed() && only == "" {
					e.o.hint("%s no está instalado, se omite", in.Harness())
					continue
				}
				plan, err := planFor(in, ep, remove)
				if err != nil {
					return fmt.Errorf("%s: %w", in.Harness(), err)
				}
				if len(plan.Changes) == 0 {
					e.o.ok("%s ya está %s", name, map[bool]string{false: "configurado", true: "sin configurar"}[remove])
					continue
				}
				e.o.println("")
				for _, ch := range plan.Changes {
					e.o.println(renderDiff(ch.Path, ch.Before, ch.After))
				}
				ok := yes
				if !ok {
					if !isTerminal() {
						return errors.New("se necesita confirmación: usa --yes en modo no interactivo")
					}
					verb := "Aplicar"
					if remove {
						verb = "Revertir"
					}
					if err := huh.NewConfirm().Title(fmt.Sprintf("¿%s los cambios en %s?", verb, in.Harness())).
						Affirmative("Sí").Negative("No").Value(&ok).Run(); err != nil {
						return err
					}
				}
				if !ok {
					e.o.warn("%s: sin cambios", name)
					continue
				}
				if err := harness.Apply(plan); err != nil {
					return err
				}
				changed = true
				e.o.ok("%s %s", name, map[bool]string{false: "configurado", true: "desconectado"}[remove])
				for _, n := range plan.Notes {
					e.o.hint("• %s", n)
				}
			}
			if remove {
				return nil
			}
			_ = changed
			return offerService(cmd.Context(), e, yes)
		},
	}
	c.Flags().BoolVarP(&yes, "yes", "y", false, "aplicar sin preguntar")
	c.Flags().BoolVar(&remove, "remove", false, "deshacer la configuración")
	c.Flags().StringVar(&only, "harness", "", "solo este harness (claude-code|opencode)")
	return c
}

func planFor(in harness.Integration, ep harness.Endpoint, remove bool) (harness.Plan, error) {
	if remove {
		return in.PlanRemove()
	}
	return in.PlanSetup(ep)
}

func filterHarness(all []harness.Integration, h domain.Harness) []harness.Integration {
	for _, in := range all {
		if in.Harness() == h {
			return []harness.Integration{in}
		}
	}
	return nil
}

// offerService proposes installing the daemon service when it is not running.
func offerService(ctx context.Context, e *env, yes bool) error {
	if _, err := e.daemon.Status(ctx); err == nil {
		return nil
	}
	st, _ := service.Get()
	if st.Installed {
		e.o.warn("El servicio del daemon está instalado pero no responde: `forge daemon logs`")
		return nil
	}
	install := yes
	if !yes && isTerminal() {
		if err := huh.NewConfirm().Title("El daemon no está en marcha. ¿Instalarlo como servicio de usuario?").
			Affirmative("Sí").Negative("No").Value(&install).Run(); err != nil {
			return err
		}
	}
	if !install {
		e.o.hint("Puedes instalarlo más tarde con `forge daemon install`.")
		return nil
	}
	return installService(e.o)
}

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnostica configuración, sesión, daemon, API y captura",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			o := out{w: cmd.OutOrStdout()}
			o.title("forge doctor")
			failures := 0
			check := func(r harness.CheckResult) {
				if r.OK {
					o.ok("%s %s", r.Name, sMuted.Render(r.Detail))
					return
				}
				failures++
				o.fail("%s %s", r.Name, r.Detail)
				if r.Fix != "" {
					o.hint("→ %s", r.Fix)
				}
			}

			e, err := load(cmd)
			if err != nil {
				check(harness.CheckResult{Name: "Configuración", Detail: err.Error(), Fix: "forge config init"})
				return errors.New("configuración inválida")
			}
			check(harness.CheckResult{Name: "Configuración", OK: true, Detail: config.File()})

			_, backend, err := e.store.Load()
			check(result("Sesión", err, string(backend), "forge login"))

			st, err := e.daemon.Status(ctx)
			if err == nil {
				detail := fmt.Sprintf("v%s · %d pendientes", st.Version, st.Pending)
				if st.LastSyncError != "" {
					check(harness.CheckResult{Name: "Daemon", Detail: "sincronización fallida: " + st.LastSyncError, Fix: "forge daemon logs"})
				} else {
					check(harness.CheckResult{Name: "Daemon", OK: true, Detail: detail})
				}
			} else {
				fix := "forge daemon install"
				if errors.Is(err, localapi.ErrDaemonDown) {
					if s, _ := service.Get(); s.Installed {
						fix = "forge daemon logs"
					}
				}
				check(harness.CheckResult{Name: "Daemon", Detail: err.Error(), Fix: fix})
			}

			check(apiReachable(ctx, e.cfg.APIURL))
			if client, err := e.api(); err == nil {
				resp, err := client.GetMeWithResponse(ctx)
				if err == nil {
					err = apiError(resp.StatusCode(), resp.JSONDefault)
				}
				check(result("API autenticada", err, "", "forge login"))
				if err == nil {
					linked := resp.JSON200.GithubLogin != nil
					check(harness.CheckResult{Name: "GitHub vinculado", OK: linked, Detail: deref(resp.JSON200.GithubLogin), Fix: "gh auth login && forge login"})
				}
			}
			_, err = devflow.GitHubToken(ctx)
			check(result("gh auth", err, "", "gh auth login"))

			ep := harness.Endpoint{BaseURL: e.cfg.IngestURL()}
			for _, in := range integrations() {
				if !in.Installed() {
					o.hint("%s no instalado", in.Harness())
					continue
				}
				for _, r := range in.Doctor(ctx, ep) {
					r.Name = string(in.Harness()) + ": " + r.Name
					check(r)
				}
			}
			o.println("")
			if failures > 0 {
				return fmt.Errorf("%d comprobaciones fallidas", failures)
			}
			o.ok("%s", sBold.Render("Todo en orden"))
			return nil
		},
	}
}

func result(name string, err error, detail, fix string) harness.CheckResult {
	if err != nil {
		return harness.CheckResult{Name: name, Detail: err.Error(), Fix: fix}
	}
	return harness.CheckResult{Name: name, OK: true, Detail: detail}
}

func apiReachable(ctx context.Context, base string) harness.CheckResult {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return harness.CheckResult{Name: "API", Detail: err.Error(), Fix: "revisa api_url en " + config.File()}
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return harness.CheckResult{Name: "API", Detail: httpStatus(resp.StatusCode)}
	}
	return harness.CheckResult{Name: "API", OK: true, Detail: base}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return "@" + *s
}
