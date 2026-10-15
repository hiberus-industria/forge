package cli

import (
	"context"
	"errors"
	"time"

	"github.com/spf13/cobra"

	"github.com/hiberus-industria/forge/internal/adapters/keycloak"
	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/apiclient"
	"github.com/hiberus-industria/forge/internal/auth"
	"github.com/hiberus-industria/forge/internal/daemon/localapi"
	"github.com/hiberus-industria/forge/internal/devflow"
)

func loginCmd() *cobra.Command {
	var device bool
	c := &cobra.Command{
		Use:   "login",
		Short: "Inicia sesión con Keycloak y vincula tu usuario de GitHub",
		Long: `Abre el navegador para autenticarte (Authorization Code + PKCE con redirect a loopback).
Si no hay navegador disponible (SSH, contenedores) usa el Device Authorization Grant.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := load(cmd)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			lc, err := keycloak.NewLoginClient(ctx, e.cfg.OIDC)
			if err != nil {
				return err
			}
			var res keycloak.LoginResult
			if device || !keycloak.CanOpenBrowser() {
				res, err = lc.LoginDevice(ctx, func(p keycloak.DevicePrompt) {
					lines := []string{
						sBold.Render("Autoriza este dispositivo"),
						"",
						"1. Abre " + sAccent.Render(p.VerificationURI),
						"2. Introduce el código " + sCode.Render(p.UserCode),
					}
					if p.VerificationURIComplete != "" {
						lines = append(lines, "", sMuted.Render("o directamente: "+p.VerificationURIComplete))
					}
					e.o.box(lines...)
					e.o.hint("Esperando autorización…")
				})
			} else {
				res, err = lc.LoginBrowser(ctx, keycloak.OpenBrowser, func(url string) {
					e.o.info("Abriendo el navegador para iniciar sesión…")
					e.o.hint("Si no se abre, visita: %s", url)
				})
			}
			if err != nil {
				return err
			}
			backend, err := keycloak.Persist(e.store, res)
			if err != nil {
				return err
			}
			e.o.ok("Sesión iniciada como %s %s", sBold.Render(res.Identity.Name), sMuted.Render("<"+res.Identity.Email+">"))
			e.o.hint("Credenciales guardadas en: %s", backend)

			linkGitHub(ctx, e, staticToken(res.Token.AccessToken, res.Token.Expiry))

			if err := e.daemon.ReloadCredentials(ctx); err == nil {
				e.o.ok("El daemon ha recargado las credenciales")
			} else if errors.Is(err, localapi.ErrDaemonDown) {
				e.o.hint("El daemon no está en marcha: `forge daemon install` para capturar el uso.")
			}
			return nil
		},
	}
	c.Flags().BoolVar(&device, "device", false, "usar el Device Authorization Grant (sin navegador local)")
	return c
}

type staticTokenSource auth.Token

func staticToken(at string, exp time.Time) auth.TokenSource {
	return staticTokenSource{AccessToken: at, ExpiresAt: exp}
}

func (s staticTokenSource) Token(context.Context) (auth.Token, error) { return auth.Token(s), nil }

// linkGitHub sends the developer's gh token once so the API can bind the
// Keycloak identity to the GitHub login. Failures are warnings: login works.
func linkGitHub(ctx context.Context, e *env, ts auth.TokenSource) {
	ghTok, err := devflow.GitHubToken(ctx)
	if err != nil {
		e.o.warn("No se pudo vincular GitHub: %v", err)
		e.o.hint("Ejecuta `gh auth login` y luego `forge login` de nuevo.")
		return
	}
	client, err := apiclient.New(e.cfg.APIURL, ts, "forge/"+version)
	if err != nil {
		e.o.warn("GitHub: %v", err)
		return
	}
	resp, err := client.LinkGitHubWithResponse(ctx, api.GitHubLinkRequest{GithubToken: &ghTok})
	if err == nil {
		err = apiError(resp.StatusCode(), resp.JSONDefault)
	}
	if err != nil {
		e.o.warn("No se pudo vincular GitHub con forge: %v", err)
		return
	}
	if u := resp.JSON200; u != nil && u.GithubLogin != nil {
		e.o.ok("GitHub vinculado: %s", sAccent.Render("@"+*u.GithubLogin))
	}
}

func logoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Cierra la sesión y borra las credenciales locales",
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := load(cmd)
			if err != nil {
				return err
			}
			if err := e.store.Delete(); err != nil {
				return err
			}
			_ = e.daemon.ReloadCredentials(cmd.Context())
			e.o.ok("Sesión cerrada")
			return nil
		},
	}
}

func whoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Muestra el usuario autenticado",
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := load(cmd)
			if err != nil {
				return err
			}
			c, backend, err := e.store.Load()
			if err != nil {
				return err
			}
			e.o.title(c.Identity.Name)
			e.o.kv("email", c.Identity.Email)
			e.o.kv("credenciales", backend)
			client, err := e.api()
			if err != nil {
				return err
			}
			resp, err := client.GetMeWithResponse(cmd.Context())
			if err == nil {
				err = apiError(resp.StatusCode(), resp.JSONDefault)
			}
			if err != nil {
				e.o.warn("API: %v", err)
				return nil
			}
			me := resp.JSON200
			gh := sWarn.Render("sin vincular")
			if me.GithubLogin != nil {
				gh = sAccent.Render("@" + *me.GithubLogin)
			}
			e.o.kv("github", gh)
			roles := ""
			for i, r := range me.Roles {
				if i > 0 {
					roles += ", "
				}
				roles += string(r)
			}
			e.o.kv("roles", roles)
			e.o.kv("api", e.cfg.APIURL)
			return nil
		},
	}
}
