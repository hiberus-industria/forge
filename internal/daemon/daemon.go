// Package daemon is the per-machine forge agent: it receives harness
// telemetry on a loopback listener, attributes every request to
// repo/branch/issue, buffers events in SQLite, syncs them to the API and
// serves the local API (localapi) to the TUI and CLI over a unix socket.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/hiberus-industria/forge/internal/adapters/claudecode"
	"github.com/hiberus-industria/forge/internal/adapters/gitrepo"
	"github.com/hiberus-industria/forge/internal/adapters/sqlite"
	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/apiclient"
	"github.com/hiberus-industria/forge/internal/auth"
	"github.com/hiberus-industria/forge/internal/config"
	"github.com/hiberus-industria/forge/internal/harness"
)

// Options wires the daemon.
type Options struct {
	Config  config.Config
	Version string
	// Session owns the developer credentials; the daemon is the only process
	// refreshing tokens. Nil behaves as logged out.
	Session auth.Session
	// Integrations are used for capture health in the status.
	Integrations []harness.Integration
	Logger       *slog.Logger
}

const (
	priceRefresh  = time.Hour
	retention     = 7 * 24 * time.Hour
	pruneInterval = time.Hour
	// reconcileEnv enables replaying Claude Code transcripts (experimental):
	// captures requests made while telemetry was off or the daemon was down.
	reconcileEnv      = "FORGE_RECONCILE_TRANSCRIPTS"
	reconcileInterval = 10 * time.Minute
)

// Daemon holds the running components.
type Daemon struct {
	cfg          config.Config
	version      string
	log          *slog.Logger
	session      auth.Session
	integrations []harness.Integration
	store        *sqlite.Store
	client       *api.ClientWithResponses
	svc          *service
	hub          *hub
	sync         *syncer
	prices       *priceCache
	machineID    string
	startedAt    time.Time
	done         chan struct{}

	meMu    sync.RWMutex
	ghLogin string
}

// Run blocks until ctx is cancelled.
func Run(ctx context.Context, opts Options) error {
	if err := opts.Config.Validate(); err != nil {
		return err
	}
	store, err := sqlite.Open(ctx, config.DBPath())
	if err != nil {
		return fmt.Errorf("open local buffer: %w", err)
	}
	defer store.Close()
	d, err := build(ctx, opts, store)
	if err != nil {
		return err
	}
	ingestLn, err := listenIngest(opts.Config.IngestAddr)
	if err != nil {
		return err
	}
	localLn, err := listenSocket(config.SocketPath())
	if err != nil {
		ingestLn.Close()
		return err
	}
	defer os.Remove(config.SocketPath())
	d.log.Info("forge daemon started", "version", opts.Version, "ingest", opts.Config.IngestURL(),
		"socket", config.SocketPath(), "api", opts.Config.APIURL)
	return d.serve(ctx, ingestLn, localLn)
}

// build wires the components around an open store.
func build(ctx context.Context, opts Options, store *sqlite.Store) (*Daemon, error) {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	session := opts.Session
	if session == nil {
		session = loggedOut{}
	}
	machineID, err := store.MachineID(ctx)
	if err != nil {
		return nil, err
	}
	client, err := apiclient.New(opts.Config.APIURL, session, "forge-daemon/"+opts.Version)
	if err != nil {
		return nil, err
	}
	d := &Daemon{
		cfg: opts.Config, version: opts.Version, log: log, session: session,
		integrations: opts.Integrations, store: store, client: client,
		hub: newHub(), prices: &priceCache{client: client},
		machineID: machineID, startedAt: time.Now().UTC(), done: make(chan struct{}),
	}
	d.sync = newSyncer(store, client, log)
	d.svc = &service{
		org: opts.Config.Org, machineID: machineID, store: store, git: gitrepo.NewResolver(),
		prices: d.prices, hub: d.hub, nudge: d.sync.nudge, log: log, projectsDir: claudecode.ProjectsDir(),
	}
	if err := d.svc.loadOverride(ctx); err != nil {
		return nil, err
	}
	if err := session.Reload(ctx); err != nil {
		log.Warn("could not load credentials", "err", err)
	}
	return d, nil
}

func (d *Daemon) serve(ctx context.Context, ingestLn, localLn net.Listener) error {
	ingest := &http.Server{Handler: d.ingestHandler(), ReadHeaderTimeout: 5 * time.Second}
	local := &http.Server{Handler: d.localHandler(), ReadHeaderTimeout: 5 * time.Second}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return serveHTTP(ingest, ingestLn) })
	g.Go(func() error { return serveHTTP(local, localLn) })
	g.Go(func() error { return d.sync.run(gctx) })
	g.Go(func() error { return d.every(gctx, priceRefresh, d.refreshRemote) })
	g.Go(func() error { return d.every(gctx, pruneInterval, d.prune) })
	if os.Getenv(reconcileEnv) == "1" {
		g.Go(func() error { return d.every(gctx, reconcileInterval, d.reconcile) })
	}
	g.Go(func() error {
		<-gctx.Done()
		close(d.done) // ends SSE streams so Shutdown does not wait on them
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = ingest.Shutdown(shutdownCtx)
		_ = local.Shutdown(shutdownCtx)
		// Best-effort final flush.
		if _, err := d.sync.flush(shutdownCtx); err != nil {
			d.log.Info("final sync skipped", "err", err)
		}
		return nil
	})
	err := g.Wait()
	d.log.Info("forge daemon stopped")
	return err
}

func serveHTTP(s *http.Server, ln net.Listener) error {
	if err := s.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// every runs fn now and then at each interval until ctx is done.
func (d *Daemon) every(ctx context.Context, interval time.Duration, fn func(context.Context)) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		fn(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// refreshRemote updates the price book and the GitHub login shown in status.
func (d *Daemon) refreshRemote(ctx context.Context) {
	if _, ok := d.session.Identity(); !ok {
		return
	}
	if err := d.prices.refresh(ctx); err != nil {
		d.log.Warn("price refresh failed", "err", err)
	}
	if resp, err := d.client.GetMeWithResponse(ctx); err == nil && resp.JSON200 != nil && resp.JSON200.GithubLogin != nil {
		d.meMu.Lock()
		d.ghLogin = *resp.JSON200.GithubLogin
		d.meMu.Unlock()
	}
}

func (d *Daemon) githubLogin() string {
	d.meMu.RLock()
	defer d.meMu.RUnlock()
	return d.ghLogin
}

// afterLogin refreshes remote data and syncs what was buffered meanwhile.
func (d *Daemon) afterLogin() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		d.refreshRemote(ctx)
		d.sync.nudge()
	}()
}

func (d *Daemon) prune(ctx context.Context) {
	if n, err := d.store.Prune(ctx, time.Now().Add(-retention)); err != nil {
		d.log.Warn("prune failed", "err", err)
	} else if n > 0 {
		d.log.Debug("pruned synced events", "count", n)
	}
}

func (d *Daemon) reconcile(ctx context.Context) {
	const key = "claude_reconciled_at"
	since := time.Now().Add(-24 * time.Hour)
	if v, err := d.store.GetKV(ctx, key); err == nil {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			since = t.Add(-time.Minute)
		}
	}
	start := time.Now().UTC()
	n, err := claudecode.Reconcile(ctx, claudecode.ProjectsDir(), since, d.svc)
	if err != nil {
		d.log.Warn("transcript reconcile failed", "err", err)
		return
	}
	_ = d.store.SetKV(ctx, key, start.Format(time.RFC3339))
	d.log.Debug("transcripts reconciled", "replayed", n)
}

// loggedOut is the Session used when none is provided.
type loggedOut struct{}

func (loggedOut) Token(context.Context) (auth.Token, error) { return auth.Token{}, auth.ErrNotLoggedIn }
func (loggedOut) Reload(context.Context) error              { return nil }
func (loggedOut) Identity() (auth.Identity, bool)           { return auth.Identity{}, false }
