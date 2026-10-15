package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/hiberus-industria/forge/internal/adapters/postgres"
	"github.com/hiberus-industria/forge/internal/app"
	"github.com/hiberus-industria/forge/internal/domain"
)

const org = "hiberus-industria"

func startStore(t *testing.T) *postgres.Store {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test (needs Docker)")
	}
	ctx := context.Background()
	var ctr *tcpostgres.PostgresContainer
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = errors.New("docker unavailable")
			}
		}()
		ctr, err = tcpostgres.Run(ctx, "postgres:17-alpine",
			tcpostgres.WithDatabase("forge"), tcpostgres.WithUsername("forge"), tcpostgres.WithPassword("forge"),
			tcpostgres.BasicWaitStrategies())
	}()
	if err != nil {
		t.Skipf("cannot start postgres container: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	url, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	s, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrations must be idempotent: %v", err)
	}
	return s
}

// fakeGitHub records project moves and serves a fixed set of issues.
type fakeGitHub struct {
	issues map[domain.IssueRef]app.IssueInfo
	moves  []domain.TaskState
}

func (f *fakeGitHub) Org() string { return org }
func (f *fakeGitHub) ListRepos(context.Context) ([]app.RepoInfo, error) {
	return []app.RepoInfo{{Ref: domain.RepoRef{Owner: org, Name: "API"}, DefaultBranch: "main"}}, nil
}

func (f *fakeGitHub) GetIssue(_ context.Context, ref domain.IssueRef) (app.IssueInfo, error) {
	if i, ok := f.issues[ref]; ok {
		return i, nil
	}
	return app.IssueInfo{}, app.ErrNotFound
}

func (f *fakeGitHub) ListIssuesUpdatedSince(context.Context, domain.RepoRef, time.Time) ([]app.IssueInfo, error) {
	var out []app.IssueInfo
	for _, i := range f.issues {
		out = append(out, i)
	}
	return out, nil
}

func (f *fakeGitHub) RepoProject(context.Context, domain.RepoRef) (*app.ProjectInfo, error) {
	return &app.ProjectInfo{ID: "P1", Title: "API board", StatusFieldID: "F1", Options: []domain.StatusOption{
		{ID: "o1", Name: "Todo"}, {ID: "o2", Name: "In Progress"}, {ID: "o3", Name: "Review"}, {ID: "o4", Name: "Done"},
	}}, nil
}

func (f *fakeGitHub) SetProjectStatus(_ context.Context, _ domain.StatusMap, _ app.IssueInfo, st domain.TaskState) error {
	f.moves = append(f.moves, st)
	return nil
}
func (f *fakeGitHub) UserFromToken(context.Context, string) (string, error) { return "dev1", nil }
func (f *fakeGitHub) IsOrgMember(context.Context, string) (bool, error)     { return true, nil }

type fakeFX struct{}

func (fakeFX) FetchRates(context.Context, time.Time) ([]domain.FXRate, error) {
	return []domain.FXRate{{Date: day(1), USDPerEUR: 1.25}, {Date: day(3), USDPerEUR: 1.0}}, nil
}

type fakePrices struct{}

func (fakePrices) FetchPrices(context.Context) ([]domain.Price, error) {
	return []domain.Price{{Provider: "anthropic", Model: "claude-sonnet-5-5", Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75, Source: domain.PriceSourceModelsDev}}, nil
}

func day(d int) time.Time { return time.Date(2026, 10, d, 12, 0, 0, 0, time.UTC) }

func as(sub string, admin bool) context.Context {
	return app.WithPrincipal(context.Background(), app.Principal{Subject: sub, Email: sub + "@example.com", Name: sub, Admin: admin})
}

func event(id string, at time.Time, in app.IssueInfo, branch string, tokens int64) domain.UsageEvent {
	repo := domain.RepoRef{Owner: "Hiberus-Industria", Name: "api"}
	return domain.UsageEvent{
		ExternalID: id, Harness: domain.HarnessClaudeCode, SessionID: "s-" + branch, Provider: "anthropic",
		Model: "claude-sonnet-5-5", Tokens: domain.Tokens{Input: tokens}, OccurredAt: at, MachineID: "m1",
		Attribution: domain.Attribute(domain.AttributionInput{Org: org, Repo: &repo, Branch: branch}),
	}
}

func TestServiceOnPostgres(t *testing.T) {
	store := startStore(t)
	issueRef := domain.IssueRef{Repo: domain.RepoRef{Owner: org, Name: "api"}, Number: 7}
	gh := &fakeGitHub{issues: map[domain.IssueRef]app.IssueInfo{
		issueRef: {Ref: issueRef, NodeID: "I7", Title: "Login OIDC", URL: "https://github.com/x/7", State: app.IssueOpen, Assignees: []string{"dev1"}},
	}}
	now := day(5)
	svc := app.New(app.Deps{Store: store, GitHub: gh, Prices: fakePrices{}, FX: fakeFX{}, Org: org, Now: func() time.Time { return now }})
	dev, other, admin := as("dev", false), as("other", false), as("admin", true)
	ctx := context.Background()

	if n, err := svc.SyncPrices(ctx); err != nil || n != 1 {
		t.Fatalf("sync prices: %d %v", n, err)
	}
	if n, _ := svc.SyncPrices(ctx); n != 0 {
		t.Fatalf("unchanged seed prices must not be duplicated, got %d", n)
	}
	if _, err := svc.RefreshFX(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.LinkGitHub(dev, "gho_x"); err != nil {
		t.Fatal(err)
	}

	// Ingest: 1M input tokens at $3/M = $3 per event.
	evs := []domain.UsageEvent{
		event("r1", day(1), app.IssueInfo{}, "feat/7-login", 1_000_000),
		event("r2", day(3), app.IssueInfo{}, "main", 1_000_000),
		event("r1", day(1), app.IssueInfo{}, "feat/7-login", 1_000_000), // duplicate in batch
	}
	res, err := svc.Ingest(dev, evs)
	if err != nil || res.Accepted != 2 || res.Duplicates != 1 {
		t.Fatalf("ingest: %+v %v", res, err)
	}
	if res, _ := svc.Ingest(dev, evs[:1]); res.Duplicates != 1 || res.Accepted != 0 {
		t.Fatalf("re-ingest must be idempotent: %+v", res)
	}
	outside := event("r3", day(1), app.IssueInfo{}, "x", 1)
	outside.Attribution = domain.Attribution{Bucket: domain.BucketRepo, Source: domain.SourceNone, Repo: &domain.RepoRef{Owner: "evil", Name: "x"}}
	if res, _ := svc.Ingest(dev, []domain.UsageEvent{outside}); len(res.Rejected) != 1 {
		t.Fatalf("repo outside org must be rejected: %+v", res)
	}

	tasks, err := svc.ListTasks(dev, app.TaskQuery{})
	if err != nil || len(tasks) != 1 || tasks[0].Cost != 3_000_000 || tasks[0].Title != "Login OIDC" {
		t.Fatalf("tasks: %+v %v", tasks, err)
	}

	// Inbox + reassignment rules.
	inbox, err := svc.Inbox(dev, app.InboxQuery{User: "me"})
	if err != nil || len(inbox) != 1 || inbox[0].Cost != 3_000_000 {
		t.Fatalf("inbox: %+v %v", inbox, err)
	}
	ids := inbox[0].EventIDs
	if r, _ := svc.Reassign(other, ids, &issueRef, "not mine"); r.Updated != 0 || r.Skipped != 1 {
		t.Fatalf("non-owner must be skipped: %+v", r)
	}
	if r, err := svc.Reassign(dev, ids, &issueRef, "triage"); err != nil || r.Updated != 1 {
		t.Fatalf("owner reassign: %+v %v", r, err)
	}
	if r, err := svc.Reassign(admin, append(ids, uuid.New()), nil, "back"); err != nil || r.Updated != 1 || r.Skipped != 1 {
		t.Fatalf("admin reassign: %+v %v", r, err)
	}
	if _, err := svc.Reassign(dev, ids, &issueRef, ""); err != nil {
		t.Fatal(err)
	}

	// Lifecycle: start mirrors status, merge snapshots, late event adjusts.
	if v, err := svc.StartTask(dev, issueRef, "feat/7-login"); err != nil || v.State != domain.TaskInProgress {
		t.Fatalf("start: %+v %v", v, err)
	}
	err = svc.HandleGitHubEvents(ctx, []app.GitHubEvent{{Kind: app.GHPRMerged, Repo: issueRef.Repo, PRNumber: 12, LinkedIssues: []int{7}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Ingest(dev, []domain.UsageEvent{event("r4", day(4), app.IssueInfo{}, "feat/7-login", 1_000_000)}); err != nil {
		t.Fatal(err)
	}
	d, err := svc.GetTask(dev, issueRef)
	if err != nil {
		t.Fatal(err)
	}
	if d.State != domain.TaskDone || d.PRNumber != 12 || d.Snapshot == nil || d.Snapshot.Cost != 6_000_000 || d.Adjustment != 3_000_000 {
		t.Fatalf("task after merge: %+v snapshot=%+v", d.TaskView, d.Snapshot)
	}
	if len(gh.moves) != 2 || gh.moves[0] != domain.TaskInProgress || gh.moves[1] != domain.TaskDone {
		t.Fatalf("project moves: %v", gh.moves)
	}
	if len(d.ByDay) != 3 || len(d.ByModel) != 1 {
		t.Fatalf("breakdowns: %+v", d)
	}

	// Reports: $3 on day1 (1.25 USD/EUR) + $3 day3 + $3 day4 (1.0) = 2.4 + 3 + 3 EUR.
	rep, err := svc.CostReport(dev, app.ReportQuery{GroupBy: app.GroupRepo, Currency: domain.CurrencyEUR})
	if err != nil || len(rep.Rows) != 1 || rep.TotalUSD != 9_000_000 || abs(rep.Total-8.4) > 1e-9 {
		t.Fatalf("report: %+v %v", rep, err)
	}

	// Manual price + reprice.
	if _, err := svc.CreatePrice(dev, domain.Price{Model: "claude-sonnet-5-5", Input: 1}); !errors.Is(err, app.ErrForbidden) {
		t.Fatalf("members cannot set prices: %v", err)
	}
	if _, err := svc.CreatePrice(admin, domain.Price{Provider: "anthropic", Model: "claude-sonnet-5-5", Input: 1, EffectiveFrom: day(1).Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.Reprice(admin, nil, ""); err != nil || n != 3 {
		t.Fatalf("reprice: %d %v", n, err)
	}
	page, err := svc.ListEvents(dev, app.EventQuery{Limit: 2})
	if err != nil || len(page.Items) != 2 || page.NextCursor == "" || page.Items[0].Cost != 1_000_000 {
		t.Fatalf("events page: %+v %v", page, err)
	}
	next, err := svc.ListEvents(dev, app.EventQuery{Limit: 2, Cursor: page.NextCursor})
	if err != nil || len(next.Items) != 1 || next.Items[0].ExternalID != "r1" {
		t.Fatalf("second page: %+v %v", next, err)
	}

	repo, err := svc.GetRepo(dev, issueRef.Repo)
	if err != nil || repo.StatusMap == nil || len(repo.StatusMap.Missing()) != 0 {
		t.Fatalf("repo: %+v %v", repo, err)
	}
	if _, err := svc.SetStatusMap(admin, issueRef.Repo, map[domain.TaskState]string{domain.TaskInReview: "nope"}); !errors.Is(err, app.ErrInvalid) {
		t.Fatalf("unknown option must be rejected: %v", err)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
