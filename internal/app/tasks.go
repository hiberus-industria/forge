package app

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/hiberus-industria/forge/internal/domain"
)

// TaskView is a task with its accumulated cost.
type TaskView struct {
	TaskRecord
	TaskCost
	// Adjustment is the cost accrued after the snapshot (closed tasks only).
	Adjustment domain.Micros
}

// TaskQuery filters ListTasks.
type TaskQuery struct {
	Assignee string // "me" (default), GitHub login, "all"
	States   []domain.TaskState
	Repo     string
}

// openStates is the default state filter.
var openStates = []domain.TaskState{domain.TaskTodo, domain.TaskInProgress, domain.TaskInReview}

// ListTasks lists tasks with cost, most recently active first. Without a
// state filter only open tasks are returned.
func (s *Service) ListTasks(ctx context.Context, q TaskQuery) ([]TaskView, error) {
	me, err := s.CurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	f := TaskFilter{States: q.States}
	if len(f.States) == 0 {
		f.States = openStates
	}
	if f.Repo, err = parseRepoFilter(q.Repo); err != nil {
		return nil, err
	}
	switch a := strings.TrimSpace(q.Assignee); a {
	case "", "me":
		if me.GitHubLogin != "" {
			f.Assignee = me.GitHubLogin
		} else {
			// Not linked yet: show the tasks the developer has spent on.
			id := me.ID
			f.SpentBy = &id
		}
	case "all":
	default:
		f.Assignee = a
	}
	recs, err := s.store.ListTasks(ctx, f)
	if err != nil {
		return nil, err
	}
	views, err := s.withCosts(ctx, recs)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(views, func(i, j int) bool { return lastActivity(views[i]).After(lastActivity(views[j])) })
	return views, nil
}

func lastActivity(v TaskView) time.Time {
	var t time.Time
	for _, c := range []*time.Time{v.LastActivity, v.StartedAt, v.PROpenedAt, v.ClosedAt} {
		if c != nil && c.After(t) {
			t = *c
		}
	}
	return t
}

func (s *Service) withCosts(ctx context.Context, recs []TaskRecord) ([]TaskView, error) {
	refs := make([]domain.IssueRef, len(recs))
	for i, r := range recs {
		refs[i] = r.Ref
	}
	costs, err := s.store.TaskCosts(ctx, refs)
	if err != nil {
		return nil, err
	}
	views := make([]TaskView, len(recs))
	for i, r := range recs {
		c := costs[r.Ref]
		views[i] = TaskView{TaskRecord: r, TaskCost: c, Adjustment: r.Adjustment(c.Cost)}
	}
	return views, nil
}

// TaskDetail is a task with cost breakdowns.
type TaskDetail struct {
	TaskView
	ByModel []ReportRow
	ByUser  []ReportRow
	ByDay   []ReportRow
}

// GetTask returns a task with breakdowns by model, user and day (USD).
func (s *Service) GetTask(ctx context.Context, ref domain.IssueRef) (TaskDetail, error) {
	if _, err := s.CurrentUser(ctx); err != nil {
		return TaskDetail{}, err
	}
	rec, err := s.loadTask(ctx, canonIssue(ref))
	if err != nil {
		return TaskDetail{}, err
	}
	views, err := s.withCosts(ctx, []TaskRecord{rec})
	if err != nil {
		return TaskDetail{}, err
	}
	d := TaskDetail{TaskView: views[0]}
	issue := rec.Ref
	for _, g := range []struct {
		by  GroupBy
		dst *[]ReportRow
	}{{GroupModel, &d.ByModel}, {GroupUser, &d.ByUser}, {GroupDay, &d.ByDay}} {
		rep, err := s.report(ctx, CostQuery{GroupBy: g.by, Issue: &issue}, domain.CurrencyUSD)
		if err != nil {
			return TaskDetail{}, err
		}
		*g.dst = rep.Rows
	}
	if g := d.ByDay; len(g) > 0 {
		sort.Slice(g, func(i, j int) bool { return g[i].Key < g[j].Key })
	}
	return d, nil
}

// loadTask returns the task, fetching it from GitHub when it is unknown or
// only a placeholder created from usage events.
func (s *Service) loadTask(ctx context.Context, ref domain.IssueRef) (TaskRecord, error) {
	rec, err := s.store.GetTask(ctx, ref)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return TaskRecord{}, err
	}
	if err == nil && !rec.Placeholder {
		return rec, nil
	}
	if s.gh == nil {
		if err != nil {
			return TaskRecord{}, err
		}
		return rec, nil
	}
	info, gerr := s.gh.GetIssue(ctx, ref)
	if gerr != nil {
		if err != nil {
			return TaskRecord{}, err // unknown locally and on GitHub
		}
		s.log.Warn("fetch placeholder issue", "issue", ref, "err", gerr)
		return rec, nil
	}
	info.Ref = canonIssue(info.Ref)
	if err := s.store.UpsertIssue(ctx, info); err != nil {
		return TaskRecord{}, err
	}
	return s.store.GetTask(ctx, ref)
}

// StartTask records the start of work on an issue (after `forge start`
// created the branch) and moves the Project status to In Progress.
func (s *Service) StartTask(ctx context.Context, ref domain.IssueRef, branch string) (TaskView, error) {
	if _, err := s.CurrentUser(ctx); err != nil {
		return TaskView{}, err
	}
	ref = canonIssue(ref)
	if !s.inOrg(ref.Repo) {
		return TaskView{}, invalid("issue %s is outside organization %s", ref, s.org)
	}
	if strings.TrimSpace(branch) == "" {
		return TaskView{}, invalid("branch is required")
	}
	return s.step(ctx, ref, domain.StepStart, func(t *TaskRecord) { t.Branch = branch })
}

// RecordPR records the pull request of an issue and moves the Project
// status to In Review.
func (s *Service) RecordPR(ctx context.Context, ref domain.IssueRef, pr int) (TaskView, error) {
	if _, err := s.CurrentUser(ctx); err != nil {
		return TaskView{}, err
	}
	if pr <= 0 {
		return TaskView{}, invalid("pr_number must be positive")
	}
	return s.step(ctx, canonIssue(ref), domain.StepPR, func(t *TaskRecord) { t.PRNumber = pr })
}

// step applies a lifecycle step, persists it and mirrors it on the Project.
func (s *Service) step(ctx context.Context, ref domain.IssueRef, st domain.Step, mutate func(*TaskRecord)) (TaskView, error) {
	rec, err := s.loadTask(ctx, ref)
	if err != nil {
		return TaskView{}, err
	}
	costs, err := s.store.TaskCosts(ctx, []domain.IssueRef{ref})
	if err != nil {
		return TaskView{}, err
	}
	c := costs[ref]
	before := rec.State
	if err := rec.Apply(st, s.now(), domain.CostSnapshot{Cost: c.Cost, Tokens: c.Tokens}); err != nil {
		return TaskView{}, errors.Join(ErrConflict, err)
	}
	if mutate != nil {
		mutate(&rec)
	}
	if err := s.store.SaveLifecycle(ctx, rec); err != nil {
		return TaskView{}, err
	}
	if rec.State != before || st == domain.StepStart {
		s.mirrorStatus(ctx, rec)
	}
	return TaskView{TaskRecord: rec, TaskCost: c, Adjustment: rec.Adjustment(c.Cost)}, nil
}

// mirrorStatus moves the issue on the repository Project. Failures are
// logged, never returned: GitHub is a mirror of forge's lifecycle here.
func (s *Service) mirrorStatus(ctx context.Context, rec TaskRecord) {
	if s.gh == nil {
		return
	}
	repo, err := s.store.GetRepo(ctx, rec.Ref.Repo)
	if err != nil || repo.StatusMap == nil || repo.StatusMap.Options[rec.State] == "" {
		return
	}
	if err := s.gh.SetProjectStatus(ctx, *repo.StatusMap, rec.IssueInfo(), rec.State); err != nil {
		s.log.Warn("set project status", "issue", rec.Ref, "state", rec.State, "err", err)
	}
}
