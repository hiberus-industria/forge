// Command forge-api is the central forge API server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/hiberus-industria/forge/internal/adapters/ecb"
	"github.com/hiberus-industria/forge/internal/adapters/github"
	"github.com/hiberus-industria/forge/internal/adapters/httpapi"
	"github.com/hiberus-industria/forge/internal/adapters/keycloak"
	"github.com/hiberus-industria/forge/internal/adapters/modelsdev"
	"github.com/hiberus-industria/forge/internal/adapters/postgres"
	"github.com/hiberus-industria/forge/internal/app"
)

var version = "dev"

type config struct {
	ListenAddr     string
	DatabaseURL    string
	OIDCIssuer     string
	OIDCAudience   string
	Org            string
	AppID          int64
	InstallationID int64
	PrivateKeyFile string
	WebhookSecret  string
	ReconcileEvery time.Duration
	LogLevel       slog.Level
}

func loadConfig() (config, error) {
	c := config{
		ListenAddr:     env("FORGE_LISTEN_ADDR", ":8080"),
		DatabaseURL:    os.Getenv("FORGE_DATABASE_URL"),
		OIDCIssuer:     os.Getenv("FORGE_OIDC_ISSUER"),
		OIDCAudience:   env("FORGE_OIDC_AUDIENCE", keycloak.DefaultAudience),
		Org:            os.Getenv("FORGE_GITHUB_ORG"),
		PrivateKeyFile: os.Getenv("FORGE_GITHUB_PRIVATE_KEY_FILE"),
		WebhookSecret:  os.Getenv("FORGE_GITHUB_WEBHOOK_SECRET"),
	}
	var errs []error
	for name, v := range map[string]string{"FORGE_DATABASE_URL": c.DatabaseURL, "FORGE_OIDC_ISSUER": c.OIDCIssuer, "FORGE_GITHUB_ORG": c.Org} {
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
	}
	var err error
	if c.AppID, err = envInt("FORGE_GITHUB_APP_ID"); err != nil {
		errs = append(errs, err)
	}
	if c.InstallationID, err = envInt("FORGE_GITHUB_INSTALLATION_ID"); err != nil {
		errs = append(errs, err)
	}
	if c.ReconcileEvery, err = time.ParseDuration(env("FORGE_RECONCILE_INTERVAL", "10m")); err != nil {
		errs = append(errs, fmt.Errorf("FORGE_RECONCILE_INTERVAL: %w", err))
	}
	if err := c.LogLevel.UnmarshalText([]byte(env("FORGE_LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, fmt.Errorf("FORGE_LOG_LEVEL: %w", err))
	}
	return c, errors.Join(errs...)
}

func (c config) githubEnabled() bool {
	return c.AppID != 0 && c.InstallationID != 0 && c.PrivateKeyFile != ""
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string) (int64, error) {
	v := os.Getenv(k)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", k, err)
	}
	return n, nil
}

func main() {
	if err := run(); err != nil {
		slog.Error("forge-api stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	verifier, err := keycloak.NewVerifier(ctx, cfg.OIDCIssuer, cfg.OIDCAudience)
	if err != nil {
		return err
	}

	deps := app.Deps{Store: store, Prices: modelsdev.New(), FX: ecb.New(), Org: cfg.Org, Logger: log}
	var webhooks app.WebhookParser
	if cfg.githubEnabled() {
		key, err := os.ReadFile(cfg.PrivateKeyFile)
		if err != nil {
			return fmt.Errorf("read github private key: %w", err)
		}
		gh, err := github.New(github.Config{Org: cfg.Org, AppID: cfg.AppID, InstallationID: cfg.InstallationID, PrivateKeyPEM: key})
		if err != nil {
			return err
		}
		deps.GitHub = gh
		if cfg.WebhookSecret != "" {
			webhooks = github.NewWebhookParser([]byte(cfg.WebhookSecret), cfg.Org)
		} else {
			log.Warn("FORGE_GITHUB_WEBHOOK_SECRET not set: webhooks disabled, relying on reconciliation")
		}
	} else {
		log.Warn("GitHub App not configured: lifecycle sync and project status disabled")
	}
	svc := app.New(deps)
	svc.RunJobs(ctx, app.Jobs{ReconcileEvery: cfg.ReconcileEvery})

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           httpapi.New(httpapi.Options{Service: svc, Verifier: verifier, Webhooks: webhooks, Logger: log}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("forge-api listening", "addr", cfg.ListenAddr, "version", version, "org", cfg.Org)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
