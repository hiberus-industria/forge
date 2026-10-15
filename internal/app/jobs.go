package app

import (
	"context"
	"time"

	"github.com/hiberus-industria/forge/internal/domain"
)

const metaLastReconcile = "github.last_reconcile"

// Reconcile is the safety net behind webhooks: it refreshes repositories,
// issues updated since the previous run and placeholder issues, and aligns
// the lifecycle with GitHub's open/closed state.
func (s *Service) Reconcile(ctx context.Context) error {
	if s.gh == nil {
		return ErrNoGitHub
	}
	started := s.now()
	if _, err := s.SyncRepos(ctx); err != nil {
		return err
	}
	var since time.Time
	if v, err := s.store.GetMeta(ctx, metaLastReconcile); err == nil && v != "" {
		since, _ = time.Parse(time.RFC3339, v)
	}
	repos, err := s.store.ListRepos(ctx)
	if err != nil {
		return err
	}
	for _, r := range repos {
		if r.Archived {
			continue
		}
		issues, err := s.gh.ListIssuesUpdatedSince(ctx, r.Ref, since)
		if err != nil {
			s.log.Warn("reconcile issues", "repo", r.Ref, "err", err)
			continue
		}
		for _, info := range issues {
			if err := s.applyIssueState(ctx, info); err != nil {
				s.log.Warn("reconcile issue", "issue", info.Ref, "err", err)
			}
		}
	}
	if err := s.fillPlaceholders(ctx); err != nil {
		s.log.Warn("reconcile placeholders", "err", err)
	}
	return s.store.SetMeta(ctx, metaLastReconcile, started.UTC().Format(time.RFC3339))
}

func (s *Service) applyIssueState(ctx context.Context, info IssueInfo) error {
	info.Ref = canonIssue(info.Ref)
	if err := s.store.UpsertIssue(ctx, info); err != nil {
		return err
	}
	rec, err := s.store.GetTask(ctx, info.Ref)
	if err != nil {
		return err
	}
	switch {
	case info.State == IssueClosed && rec.State != domain.TaskDone:
		_, err = s.step(ctx, info.Ref, domain.StepClose, nil)
	case info.State == IssueOpen && rec.State == domain.TaskDone:
		_, err = s.step(ctx, info.Ref, domain.StepReopen, nil)
	}
	return err
}

func (s *Service) fillPlaceholders(ctx context.Context) error {
	refs, err := s.store.Placeholders(ctx, 50)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		info, err := s.gh.GetIssue(ctx, ref)
		if err != nil {
			s.log.Debug("placeholder issue not found", "issue", ref, "err", err)
			continue
		}
		if err := s.applyIssueState(ctx, info); err != nil {
			return err
		}
	}
	return nil
}

// Jobs configures the background loops of the API server.
type Jobs struct {
	ReconcileEvery time.Duration // default 10m
	PricesEvery    time.Duration // default 24h
	FXEvery        time.Duration // default 24h
}

// RunJobs runs the reconciler, price sync and FX refresh until ctx is done.
// Each loop runs once at start-up. Disabled integrations are skipped.
func (s *Service) RunJobs(ctx context.Context, j Jobs) {
	if j.ReconcileEvery <= 0 {
		j.ReconcileEvery = 10 * time.Minute
	}
	if j.PricesEvery <= 0 {
		j.PricesEvery = 24 * time.Hour
	}
	if j.FXEvery <= 0 {
		j.FXEvery = 24 * time.Hour
	}
	if s.gh != nil {
		go s.loop(ctx, "reconcile", j.ReconcileEvery, s.Reconcile)
	}
	if s.prices != nil {
		go s.loop(ctx, "prices", j.PricesEvery, func(ctx context.Context) error {
			n, err := s.SyncPrices(ctx)
			if err == nil {
				s.log.Info("prices synced", "inserted", n)
			}
			return err
		})
	}
	if s.fx != nil {
		go s.loop(ctx, "fx", j.FXEvery, func(ctx context.Context) error {
			_, err := s.RefreshFX(ctx)
			return err
		})
	}
}

func (s *Service) loop(ctx context.Context, name string, every time.Duration, fn func(context.Context) error) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		runCtx, cancel := context.WithTimeout(ctx, every)
		if err := fn(runCtx); err != nil && ctx.Err() == nil {
			s.log.Warn("background job failed", "job", name, "err", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
