package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hiberus-industria/forge/internal/adapters/claudecode"
	"github.com/hiberus-industria/forge/internal/adapters/gitrepo"
	"github.com/hiberus-industria/forge/internal/adapters/sqlite"
	"github.com/hiberus-industria/forge/internal/daemon/localapi"
	"github.com/hiberus-industria/forge/internal/domain"
	"github.com/hiberus-industria/forge/internal/harness"
)

const overrideKey = "override"

// service is the harness.Sink: it attributes captured requests and buffers them.
type service struct {
	org         string
	machineID   string
	store       *sqlite.Store
	git         *gitrepo.Resolver
	prices      *priceCache
	hub         *hub
	nudge       func()
	log         *slog.Logger
	projectsDir string // Claude Code transcripts, for the session cwd fallback

	mu       sync.RWMutex
	override *localapi.Override
}

var _ harness.Sink = (*service)(nil)

func (s *service) loadOverride(ctx context.Context) error {
	var o localapi.Override
	found, err := s.store.GetJSON(ctx, overrideKey, &o)
	if err != nil || !found {
		return err
	}
	s.mu.Lock()
	s.override = &o
	s.mu.Unlock()
	return nil
}

// SessionStarted records the working directory of a session.
func (s *service) SessionStarted(ctx context.Context, ss harness.SessionStarted) error {
	if ss.SessionID == "" || ss.Cwd == "" {
		return nil
	}
	return s.store.UpsertSession(ctx, ss.Harness, ss.SessionID, ss.Cwd, ss.At)
}

// Capture attributes a request to repo/branch/issue and buffers it.
func (s *service) Capture(ctx context.Context, c harness.Captured) error {
	cwd, err := s.sessionCwd(ctx, c)
	if err != nil {
		return err
	}
	in := domain.AttributionInput{Org: s.org}
	top := cwd
	if cwd != "" {
		if info, err := s.git.Resolve(cwd); err == nil {
			in.Repo, in.Branch, top = info.Repo, info.Branch, info.TopLevel
		}
	}
	in.Override = s.overrideFor(top)

	ev := domain.UsageEvent{
		ExternalID: c.ExternalID, Harness: c.Harness, SessionID: c.SessionID,
		Provider: c.Provider, Model: c.Model, Tokens: c.Tokens, OccurredAt: c.OccurredAt.UTC(),
		ReportedCost: c.ReportedCost, MachineID: s.machineID,
		Attribution: domain.Attribute(in),
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now().UTC()
	}
	if err := ev.Validate(); err != nil {
		return fmt.Errorf("invalid %s event %s: %w", c.Harness, c.ExternalID, err)
	}
	estimate := s.prices.estimate(ev)
	inserted, err := s.store.InsertEvent(ctx, ev, estimate)
	if err != nil || !inserted {
		return err
	}
	s.log.Debug("captured", "harness", ev.Harness, "model", ev.Model, "bucket", ev.Attribution.Bucket, "cost", estimate)
	s.hub.publish(localapi.LiveEvent{
		Harness: ev.Harness, SessionID: ev.SessionID, Provider: ev.Provider, Model: ev.Model,
		Tokens: ev.Tokens, CostMicros: estimate, OccurredAt: ev.OccurredAt, Attribution: ev.Attribution,
	})
	if s.nudge != nil {
		s.nudge()
	}
	return nil
}

func (s *service) sessionCwd(ctx context.Context, c harness.Captured) (string, error) {
	if c.Cwd != "" {
		return c.Cwd, s.store.UpsertSession(ctx, c.Harness, c.SessionID, c.Cwd, c.OccurredAt)
	}
	if c.SessionID == "" {
		return "", nil
	}
	cwd, err := s.store.SessionCwd(ctx, c.Harness, c.SessionID)
	if err != nil || cwd != "" {
		return cwd, err
	}
	if c.Harness == domain.HarnessClaudeCode && s.projectsDir != "" {
		if cwd = claudecode.SessionCwd(s.projectsDir, c.SessionID); cwd != "" {
			return cwd, s.store.UpsertSession(ctx, c.Harness, c.SessionID, cwd, c.OccurredAt)
		}
	}
	return "", nil
}

func (s *service) overrideFor(dir string) *domain.IssueRef {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o := s.override
	if o == nil {
		return nil
	}
	if o.Scope == localapi.ScopeGlobal || (dir != "" && within(o.RepoPath, dir)) {
		issue := o.Issue
		return &issue
	}
	return nil
}

func within(root, dir string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

var errBadOverride = errors.New("invalid override")

// setOverride validates and persists the developer's manual task.
func (s *service) setOverride(ctx context.Context, r localapi.OverrideRequest) (localapi.Override, error) {
	if r.Issue.Number <= 0 || r.Issue.Repo.Owner == "" || r.Issue.Repo.Name == "" {
		return localapi.Override{}, fmt.Errorf("%w: issue must be owner/name#number", errBadOverride)
	}
	o := localapi.Override{Issue: r.Issue, Scope: r.Scope, SetAt: time.Now().UTC()}
	switch r.Scope {
	case localapi.ScopeGlobal:
	case localapi.ScopeRepo:
		if r.RepoPath == "" || !filepath.IsAbs(r.RepoPath) {
			return o, fmt.Errorf("%w: repo scope needs an absolute repo_path", errBadOverride)
		}
		o.RepoPath = filepath.Clean(r.RepoPath)
		if info, err := s.git.Resolve(r.RepoPath); err == nil {
			o.RepoPath = info.TopLevel
		}
	default:
		return o, fmt.Errorf("%w: unknown scope %q", errBadOverride, r.Scope)
	}
	if err := s.store.SetJSON(ctx, overrideKey, o); err != nil {
		return o, err
	}
	s.mu.Lock()
	s.override = &o
	s.mu.Unlock()
	return o, nil
}

func (s *service) clearOverride(ctx context.Context) error {
	if err := s.store.DeleteKV(ctx, overrideKey); err != nil {
		return err
	}
	s.mu.Lock()
	s.override = nil
	s.mu.Unlock()
	return nil
}

func (s *service) currentOverride() *localapi.Override {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.override == nil {
		return nil
	}
	o := *s.override
	return &o
}
