package cli

import (
	"errors"
	"net/url"

	"charm.land/huh/v2"
	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"

	"github.com/hiberus-industria/forge/internal/config"
)

func configCmd() *cobra.Command {
	c := &cobra.Command{Use: "config", Short: "Muestra o crea la configuración"}
	c.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Muestra la configuración efectiva",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			o := out{w: cmd.OutOrStdout()}
			o.title("Configuración")
			o.hint("%s", config.File())
			b, _ := toml.Marshal(cfg)
			o.println(string(b))
			o.kv("socket", config.SocketPath())
			o.kv("estado", config.StateDir())
			return nil
		},
	}, &cobra.Command{
		Use:   "init",
		Short: "Crea o edita la configuración de forma interactiva",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				cfg = config.Defaults()
			}
			form := huh.NewForm(
				huh.NewGroup(
					huh.NewNote().Title("⚒ forge").Description("Configuración del entorno de desarrollo"),
					huh.NewInput().Title("URL de la API de forge").Value(&cfg.APIURL).Validate(validURL),
					huh.NewInput().Title("Issuer de Keycloak").Description("https://<host>/realms/<realm>").Value(&cfg.OIDC.Issuer).Validate(validURL),
					huh.NewInput().Title("Client ID público").Value(&cfg.OIDC.ClientID).Validate(required),
					huh.NewInput().Title("Organización de GitHub").Value(&cfg.Org).Validate(required),
					huh.NewSelect[string]().Title("Moneda preferida").Options(huh.NewOptions("EUR", "USD")...).Value(&cfg.Currency),
				),
			)
			if err := form.RunWithContext(cmd.Context()); err != nil {
				return err
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			if err := config.Save(cfg); err != nil {
				return err
			}
			out{w: cmd.OutOrStdout()}.ok("Guardado en %s", config.File())
			return nil
		},
	})
	return c
}

func required(s string) error {
	if s == "" {
		return errors.New("obligatorio")
	}
	return nil
}

func validURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New("URL no válida")
	}
	return nil
}
