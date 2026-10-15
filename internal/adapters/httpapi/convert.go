package httpapi

import (
	"fmt"

	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/app"
	"github.com/hiberus-industria/forge/internal/domain"
)

func ptr[T any](v T) *T { return &v }

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func toTokens(t domain.Tokens) api.Tokens {
	return api.Tokens{Input: t.Input, Output: t.Output, Reasoning: t.Reasoning, CacheRead: t.CacheRead, CacheWrite: t.CacheWrite}
}

func fromTokens(t api.Tokens) domain.Tokens {
	return domain.Tokens{Input: t.Input, Output: t.Output, Reasoning: t.Reasoning, CacheRead: t.CacheRead, CacheWrite: t.CacheWrite}
}

func toUser(m app.Me) api.User {
	roles := []api.UserRoles{"member"}
	if m.Admin {
		roles = append(roles, "admin")
	}
	return api.User{Id: m.ID, Email: m.Email, Name: m.Name, GithubLogin: optStr(m.GitHubLogin), Roles: roles}
}

func toUserSummary(u app.UserRef) api.UserSummary {
	return api.UserSummary{Id: u.ID, Name: u.Name, GithubLogin: optStr(u.GitHubLogin)}
}

func toIssueRef(i domain.IssueRef) api.IssueRef {
	return api.IssueRef{Repo: i.Repo.String(), Number: i.Number}
}

func fromIssueRef(i api.IssueRef) (domain.IssueRef, error) {
	r, err := domain.ParseRepoRef(i.Repo)
	if err != nil {
		return domain.IssueRef{}, fmt.Errorf("%w: %w", app.ErrInvalid, err)
	}
	return domain.IssueRef{Repo: r, Number: i.Number}, nil
}

func toAttribution(a domain.Attribution) api.Attribution {
	out := api.Attribution{Bucket: api.Bucket(a.Bucket), Source: api.AttributionSource(a.Source), Branch: optStr(a.Branch)}
	if a.Repo != nil {
		out.Repo = ptr(a.Repo.String())
	}
	if a.Issue != nil {
		out.Issue = ptr(toIssueRef(*a.Issue))
	}
	return out
}

func fromAttribution(a api.Attribution) (domain.Attribution, error) {
	out := domain.Attribution{Bucket: domain.Bucket(a.Bucket), Source: domain.AttributionSource(a.Source), Branch: deref(a.Branch)}
	if a.Repo != nil && *a.Repo != "" {
		r, err := domain.ParseRepoRef(*a.Repo)
		if err != nil {
			return out, err
		}
		out.Repo = &r
	}
	if a.Issue != nil {
		i, err := fromIssueRef(*a.Issue)
		if err != nil {
			return out, err
		}
		out.Issue = &i
	}
	return out, nil
}

func fromEventInput(in api.UsageEventInput) (domain.UsageEvent, error) {
	attr, err := fromAttribution(in.Attribution)
	if err != nil {
		return domain.UsageEvent{}, err
	}
	return domain.UsageEvent{
		ExternalID: in.ExternalId, Harness: domain.Harness(in.Harness), SessionID: in.SessionId,
		Provider: deref(in.Provider), Model: in.Model, Tokens: fromTokens(in.Tokens), OccurredAt: in.OccurredAt,
		ReportedCost: domain.Micros(deref(in.ReportedCostUsdMicros)), MachineID: in.MachineId, Attribution: attr,
	}, nil
}

func toEvent(e app.StoredEvent) api.UsageEvent {
	return api.UsageEvent{
		Id: e.ID, ExternalId: e.ExternalID, Harness: api.Harness(e.Harness), SessionId: e.SessionID,
		Provider: optStr(e.Provider), Model: e.Model, Tokens: toTokens(e.Tokens), OccurredAt: e.OccurredAt,
		ReceivedAt: e.ReceivedAt, User: toUserSummary(e.User), Attribution: toAttribution(e.Attribution),
		CostUsdMicros: int64(e.Cost), ReportedCostUsdMicros: ptr(int64(e.ReportedCost)), Priced: e.Priced,
	}
}

func toInboxGroup(g app.InboxGroup) api.InboxGroup {
	out := api.InboxGroup{
		Bucket: api.Bucket(g.Bucket), Branch: optStr(g.Branch), Harness: api.Harness(g.Harness),
		SessionId: g.SessionID, User: toUserSummary(g.User), FirstAt: g.FirstAt, LastAt: g.LastAt,
		Events: g.Events, CostUsdMicros: int64(g.Cost), EventIds: g.EventIDs,
	}
	if len(g.Models) > 0 {
		out.Models = &g.Models
	}
	if g.Repo != nil {
		out.Repo = ptr(g.Repo.String())
	}
	return out
}

func toTask(v app.TaskView) api.Task {
	t := api.Task{
		Ref: toIssueRef(v.Ref), Title: v.Title, Url: v.URL, State: api.TaskState(v.State),
		Assignees: nonNil(v.Assignees), Labels: nonNil(v.Labels), Branch: optStr(v.Branch),
		StartedAt: v.StartedAt, PrOpenedAt: v.PROpenedAt, ClosedAt: v.ClosedAt,
		CostUsdMicros: int64(v.Cost), Tokens: toTokens(v.Tokens), Events: v.Events, LastActivityAt: v.LastActivity,
	}
	if t.State == "" {
		t.State = api.TaskState(domain.TaskTodo)
	}
	if v.PRNumber > 0 {
		t.PrNumber = ptr(v.PRNumber)
	}
	if v.Snapshot != nil {
		t.SnapshotUsdMicros = ptr(int64(v.Snapshot.Cost))
		t.AdjustmentUsdMicros = ptr(int64(v.Adjustment))
	}
	return t
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func toRows(rows []app.ReportRow) []api.CostRow {
	out := make([]api.CostRow, len(rows))
	for i, r := range rows {
		out[i] = api.CostRow{Key: r.Key, Label: r.Label, Cost: r.Amount, CostUsdMicros: int64(r.Cost), Tokens: toTokens(r.Tokens), Events: r.Events}
	}
	return out
}

func toRepo(r app.Repo) api.Repository {
	out := api.Repository{FullName: r.Ref.String(), DefaultBranch: r.DefaultBranch, Archived: r.Archived}
	if r.Project != nil {
		out.Project = &struct {
			Id    string `json:"id"`
			Title string `json:"title"`
			Url   string `json:"url"`
		}{Id: r.Project.ID, Title: r.Project.Title, Url: r.Project.URL}
		opts := make([]api.StatusOption, len(r.Project.Options))
		for i, o := range r.Project.Options {
			opts[i] = api.StatusOption{Id: o.ID, Name: o.Name}
		}
		out.StatusOptions = &opts
	}
	if r.StatusMap != nil {
		sm := r.StatusMap.Options
		out.StatusMap = &api.StatusMapOptions{
			Todo: optStr(sm[domain.TaskTodo]), InProgress: optStr(sm[domain.TaskInProgress]),
			InReview: optStr(sm[domain.TaskInReview]), Done: optStr(sm[domain.TaskDone]),
		}
		out.StatusMapDetected = ptr(r.StatusMap.Detected)
		missing := []api.TaskState{}
		for _, st := range r.StatusMap.Missing() {
			missing = append(missing, api.TaskState(st))
		}
		out.MissingStates = &missing
	}
	return out
}

func toPrice(p domain.Price) api.Price {
	return api.Price{
		Id: p.ID, Provider: optStr(p.Provider), Model: p.Model, Input: p.Input, Output: p.Output,
		CacheRead: p.CacheRead, CacheWrite: p.CacheWrite, EffectiveFrom: p.EffectiveFrom, Source: api.PriceSource(p.Source),
	}
}

func mapSlice[T, U any](in []T, f func(T) U) []U {
	out := make([]U, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}
