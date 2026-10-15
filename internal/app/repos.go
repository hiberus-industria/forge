package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/hiberus-industria/forge/internal/domain"
)

// ListRepos returns the organization repositories known to forge.
func (s *Service) ListRepos(ctx context.Context) ([]Repo, error) {
	if _, err := s.CurrentUser(ctx); err != nil {
		return nil, err
	}
	return s.store.ListRepos(ctx)
}

// GetRepo returns one repository.
func (s *Service) GetRepo(ctx context.Context, ref domain.RepoRef) (Repo, error) {
	if _, err := s.CurrentUser(ctx); err != nil {
		return Repo{}, err
	}
	return s.store.GetRepo(ctx, canonRepo(ref))
}

// SetStatusMap overrides the detected Project status mapping (admin).
// Every option id must belong to the repository Project's Status field.
func (s *Service) SetStatusMap(ctx context.Context, ref domain.RepoRef, opts map[domain.TaskState]string) (Repo, error) {
	if _, err := s.requireAdmin(ctx); err != nil {
		return Repo{}, err
	}
	ref = canonRepo(ref)
	r, err := s.store.GetRepo(ctx, ref)
	if err != nil {
		return Repo{}, err
	}
	if r.Project == nil {
		return Repo{}, fmt.Errorf("%w: %s has no linked GitHub Project", ErrConflict, ref)
	}
	valid := map[string]bool{}
	for _, o := range r.Project.Options {
		valid[o.ID] = true
	}
	sm := domain.StatusMap{ProjectID: r.Project.ID, StatusFieldID: r.Project.StatusFieldID, Options: map[domain.TaskState]string{}}
	for st, id := range opts {
		if id == "" {
			continue
		}
		if !valid[id] {
			return Repo{}, invalid("option %q is not a status of project %q", id, r.Project.Title)
		}
		sm.Options[st] = id
	}
	if err := s.store.SaveStatusMap(ctx, ref, sm); err != nil {
		return Repo{}, err
	}
	r.StatusMap = &sm
	return r, nil
}

// SyncRepos refreshes every organization repository and its Project.
func (s *Service) SyncRepos(ctx context.Context) (int, error) {
	if s.gh == nil {
		return 0, ErrNoGitHub
	}
	infos, err := s.gh.ListRepos(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, info := range infos {
		if err := s.syncRepo(ctx, info); err != nil {
			s.log.Warn("sync repo", "repo", info.Ref, "err", err)
			continue
		}
		n++
	}
	return n, nil
}

// syncRepoRef refreshes one repository (webhook repo_changed).
func (s *Service) syncRepoRef(ctx context.Context, ref domain.RepoRef) error {
	infos, err := s.gh.ListRepos(ctx)
	if err != nil {
		return err
	}
	for _, info := range infos {
		if canonRepo(info.Ref) == canonRepo(ref) {
			return s.syncRepo(ctx, info)
		}
	}
	return nil
}

func (s *Service) syncRepo(ctx context.Context, info RepoInfo) error {
	info.Ref = canonRepo(info.Ref)
	r := Repo{RepoInfo: info}
	existing, err := s.store.GetRepo(ctx, info.Ref)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if !info.Archived {
		p, err := s.gh.RepoProject(ctx, info.Ref)
		if err != nil {
			return err
		}
		r.Project = p
	}
	if r.Project != nil {
		r.StatusMap = mergeStatusMap(existing.StatusMap, r.Project)
	}
	now := s.now()
	r.SyncedAt = &now
	return s.store.UpsertRepo(ctx, r)
}

// mergeStatusMap keeps an admin override as long as it still points to the
// same Project and its options still exist; otherwise it re-detects.
func mergeStatusMap(prev *domain.StatusMap, p *ProjectInfo) *domain.StatusMap {
	detected := domain.DetectStatusMap(p.ID, p.StatusFieldID, p.Options)
	if prev == nil || prev.Detected || prev.ProjectID != p.ID {
		return &detected
	}
	valid := map[string]bool{}
	for _, o := range p.Options {
		valid[o.ID] = true
	}
	kept := domain.StatusMap{ProjectID: p.ID, StatusFieldID: p.StatusFieldID, Options: map[domain.TaskState]string{}}
	for st, id := range prev.Options {
		if valid[id] {
			kept.Options[st] = id
		} else if d := detected.Options[st]; d != "" {
			kept.Options[st] = d
		}
	}
	return &kept
}
