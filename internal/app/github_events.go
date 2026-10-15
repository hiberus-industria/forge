package app

import (
	"context"
	"errors"

	"github.com/hiberus-industria/forge/internal/domain"
)

// HandleGitHubEvents applies webhook deliveries. Unknown repositories
// outside the organization are ignored. It processes every event and
// returns the joined errors, so one bad event does not block the rest.
func (s *Service) HandleGitHubEvents(ctx context.Context, evs []GitHubEvent) error {
	var errs []error
	for _, e := range evs {
		if !s.inOrg(e.Repo) {
			continue
		}
		if err := s.handleGitHubEvent(ctx, e); err != nil {
			s.log.Warn("github event", "kind", e.Kind, "repo", e.Repo, "err", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) handleGitHubEvent(ctx context.Context, e GitHubEvent) error {
	repo := canonRepo(e.Repo)
	switch e.Kind {
	case GHRepoChanged:
		if s.gh == nil {
			return nil
		}
		if repo.Name == "" {
			// Project item changes do not tell which repository they touch.
			_, err := s.SyncRepos(ctx)
			return err
		}
		return s.syncRepoRef(ctx, repo)
	case GHIssueUpserted, GHIssueClosed, GHIssueReopened:
		if e.Issue == nil {
			return nil
		}
		info := *e.Issue
		info.Ref = canonIssue(info.Ref)
		if err := s.store.UpsertIssue(ctx, info); err != nil {
			return err
		}
		switch e.Kind {
		case GHIssueClosed:
			_, err := s.step(ctx, info.Ref, domain.StepClose, nil)
			return err
		case GHIssueReopened:
			_, err := s.step(ctx, info.Ref, domain.StepReopen, nil)
			return err
		}
		return nil
	case GHPROpened, GHPRMerged:
		st := domain.StepPR
		if e.Kind == GHPRMerged {
			st = domain.StepMerge
		}
		var errs []error
		for _, n := range e.LinkedIssues {
			ref := domain.IssueRef{Repo: repo, Number: n}
			pr := e.PRNumber
			_, err := s.step(ctx, ref, st, func(t *TaskRecord) {
				if t.PRNumber == 0 && pr > 0 {
					t.PRNumber = pr
				}
			})
			if err != nil && !errors.Is(err, ErrNotFound) {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
	return nil
}
