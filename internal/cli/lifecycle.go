package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hiberus-industria/forge/internal/config"
	"github.com/hiberus-industria/forge/internal/daemon/localapi"
	"github.com/hiberus-industria/forge/internal/devflow"
	"github.com/hiberus-industria/forge/internal/domain"
	"github.com/hiberus-industria/forge/internal/tui"
)

func (e *env) flow() (*devflow.Flow, error) {
	client, err := e.api()
	if err != nil {
		return nil, err
	}
	return devflow.New(client), nil
}

func startCmd() *cobra.Command {
	var typ string
	c := &cobra.Command{
		Use:   "start <issue>",
		Short: "Empieza a trabajar en un issue: rama, asignación y estado In Progress",
		Example: `  forge start 123
  forge start api#45 --type fix
  forge start hiberus-industria/web#7`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := load(cmd)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			ref, err := parseIssueArg(ctx, e.cfg.Org, args[0])
			if err != nil {
				return err
			}
			if typ != "" && !validBranchType(domain.BranchType(typ)) {
				return fmt.Errorf("tipo de rama %q no válido (%v)", typ, domain.BranchTypes)
			}
			flow, err := e.flow()
			if err != nil {
				return err
			}
			res, err := flow.Start(ctx, devflow.StartOptions{
				Issue: ref, Type: domain.BranchType(typ),
				Progress: func(s string) { e.o.info("%s", s) },
			})
			if res.Branch != "" {
				verb := "Rama creada"
				if !res.Created {
					verb = "Rama existente"
				}
				e.o.box(
					sBold.Render(ref.String())+"  "+res.Title,
					sMuted.Render(verb+": ")+sAccent.Render(res.Branch),
				)
			}
			if err != nil {
				return err
			}
			e.o.ok("Todo el gasto IA en esta rama se imputará a %s", sBold.Render(ref.String()))
			return nil
		},
	}
	c.Flags().StringVarP(&typ, "type", "t", "", "tipo de rama (feat, fix, chore, docs, refactor, test, spike); por defecto según las labels")
	return c
}

func validBranchType(t domain.BranchType) bool {
	for _, b := range domain.BranchTypes {
		if b == t {
			return true
		}
	}
	return false
}

func prCmd() *cobra.Command {
	var (
		draft       bool
		title, body string
		issue       string
	)
	c := &cobra.Command{
		Use:   "pr",
		Short: "Sube la rama, abre el PR enlazado al issue y pasa a In Review",
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := load(cmd)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			opts := devflow.PROptions{Draft: draft, Title: title, Body: body, Progress: func(s string) { e.o.info("%s", s) }}
			if issue != "" {
				ref, err := parseIssueArg(ctx, e.cfg.Org, issue)
				if err != nil {
					return err
				}
				opts.Issue = &ref
			}
			flow, err := e.flow()
			if err != nil {
				return err
			}
			res, err := flow.OpenPR(ctx, opts)
			if res.Number > 0 {
				label := "PR abierto"
				if res.Existing {
					label = "PR existente"
				}
				e.o.box(sBold.Render(fmt.Sprintf("%s #%d", label, res.Number)), sAccent.Render(res.URL))
			}
			return err
		},
	}
	c.Flags().BoolVar(&draft, "draft", false, "abrir como borrador")
	c.Flags().StringVar(&title, "title", "", "título (por defecto el del issue)")
	c.Flags().StringVar(&body, "body", "", "descripción (se añade 'Closes #N')")
	c.Flags().StringVar(&issue, "issue", "", "issue a enlazar (por defecto, el de la rama)")
	return c
}

func taskCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "task",
		Short: "Fuerza a mano la tarea a la que se imputa el gasto",
	}
	var global bool
	set := &cobra.Command{
		Use:   "set <issue>",
		Short: "Imputa el gasto de este repo (o de toda la máquina con --global) a un issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := load(cmd)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			ref, err := parseIssueArg(ctx, e.cfg.Org, args[0])
			if err != nil {
				return err
			}
			req := localapi.OverrideRequest{Issue: ref, Scope: localapi.ScopeGlobal}
			if !global {
				top, _, err := localRepo(ctx)
				if err != nil {
					return fmt.Errorf("%w (usa --global fuera de un repositorio)", err)
				}
				req.Scope, req.RepoPath = localapi.ScopeRepo, top
			}
			ov, err := e.daemon.SetOverride(ctx, req)
			if err != nil {
				return err
			}
			where := "toda la máquina"
			if ov.Scope == localapi.ScopeRepo {
				where = ov.RepoPath
			}
			e.o.ok("Gasto de %s → %s", sMuted.Render(where), sBold.Render(ov.Issue.String()))
			return nil
		},
	}
	set.Flags().BoolVar(&global, "global", false, "aplicar a todas las sesiones de la máquina")
	clear := &cobra.Command{
		Use:   "clear",
		Short: "Vuelve a la atribución por rama",
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := load(cmd)
			if err != nil {
				return err
			}
			if err := e.daemon.ClearOverride(cmd.Context()); err != nil {
				return err
			}
			e.o.ok("Atribución por rama restablecida")
			return nil
		},
	}
	c.AddCommand(set, clear)
	return c
}

func tuiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Abre la interfaz de terminal",
		RunE:  func(cmd *cobra.Command, _ []string) error { return runTUI(cmd.Context()) },
	}
}

func runTUI(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	e := &env{cfg: cfg, daemon: localapi.NewClient(config.SocketPath())}
	e.store = credstoreFor()
	client, err := e.api()
	if err != nil {
		return err
	}
	return tui.Run(ctx, tui.Deps{
		Config:  cfg,
		Version: version,
		Daemon:  e.daemon,
		API:     client,
		Flow:    devflow.New(client),
	})
}
