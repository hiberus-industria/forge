package devflow

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/google/go-github/v92/github"

	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/domain"
)

// Flow implements Service with local git, the developer's GitHub identity
// and the forge API.
type Flow struct {
	api      *api.ClientWithResponses
	token    func(context.Context) (string, error)
	ghClient func(token string) (*github.Client, error)
}

var _ Service = (*Flow)(nil)

// Option customizes a Flow.
type Option func(*Flow)

// WithGitHubBaseURL points the GitHub client somewhere else (tests, GHES).
func WithGitHubBaseURL(url string) Option {
	return func(f *Flow) {
		f.ghClient = func(tok string) (*github.Client, error) {
			return github.NewClient(github.WithAuthToken(tok), github.WithEnterpriseURLs(url, url))
		}
	}
}

// WithTokenFunc overrides how the developer GitHub token is obtained.
func WithTokenFunc(fn func(context.Context) (string, error)) Option {
	return func(f *Flow) { f.token = fn }
}

// New returns the lifecycle service.
func New(apiClient *api.ClientWithResponses, opts ...Option) *Flow {
	f := &Flow{
		api:      apiClient,
		token:    GitHubToken,
		ghClient: func(tok string) (*github.Client, error) { return github.NewClient(github.WithAuthToken(tok)) },
	}
	for _, o := range opts {
		o(f)
	}
	return f
}

type workspace struct {
	git  git
	repo domain.RepoRef
	gh   *github.Client
}

func (f *Flow) open(ctx context.Context, dir string) (workspace, error) {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return workspace{}, err
		}
		dir = wd
	}
	g := git{dir: dir}
	top, err := g.topLevel(ctx)
	if err != nil {
		return workspace{}, err
	}
	g.dir = top
	repo, err := g.originRepo(ctx)
	if err != nil {
		return workspace{}, err
	}
	tok, err := f.token(ctx)
	if err != nil {
		return workspace{}, err
	}
	gh, err := f.ghClient(tok)
	if err != nil {
		return workspace{}, err
	}
	return workspace{git: g, repo: repo, gh: gh}, nil
}

func progress(fn func(string), format string, a ...any) {
	if fn != nil {
		fn(fmt.Sprintf(format, a...))
	}
}

// Start creates (or reuses) the issue branch, assigns the issue to the
// developer and records the start in forge, which moves the Project status.
func (f *Flow) Start(ctx context.Context, opts StartOptions) (StartResult, error) {
	ws, err := f.open(ctx, opts.RepoPath)
	if err != nil {
		return StartResult{}, err
	}
	ref := opts.Issue
	if !ref.Repo.Equal(ws.repo) {
		return StartResult{}, fmt.Errorf("%s belongs to %s but this clone is %s", ref, ref.Repo, ws.repo)
	}

	progress(opts.Progress, "Leyendo %s", ref)
	issue, _, err := ws.gh.Issues.Get(ctx, ref.Repo.Owner, ref.Repo.Name, ref.Number)
	if err != nil {
		return StartResult{}, fmt.Errorf("get issue %s: %w", ref, err)
	}
	if issue.IsPullRequest() {
		return StartResult{}, fmt.Errorf("%s is a pull request, not an issue", ref)
	}
	if issue.GetState() == "closed" {
		return StartResult{}, fmt.Errorf("%s is closed; reopen it in GitHub first", ref)
	}
	res := StartResult{Title: issue.GetTitle()}

	progress(opts.Progress, "Preparando rama")
	res.Branch, res.Created, err = f.checkoutIssueBranch(ctx, ws, opts, issue)
	if err != nil {
		return res, err
	}

	me, _, err := ws.gh.Users.Get(ctx, "")
	if err != nil {
		return res, fmt.Errorf("resolve GitHub user: %w", err)
	}
	if !assigned(issue, me.GetLogin()) {
		progress(opts.Progress, "Asignando %s a @%s", ref, me.GetLogin())
		if _, _, err := ws.gh.Issues.AddAssignees(ctx, ref.Repo.Owner, ref.Repo.Name, ref.Number, []string{me.GetLogin()}); err != nil {
			return res, fmt.Errorf("assign issue: %w", err)
		}
	}

	progress(opts.Progress, "Registrando inicio en forge")
	resp, err := f.api.StartTaskWithResponse(ctx, ref.Repo.Owner, ref.Repo.Name, ref.Number, api.StartTaskRequest{Branch: res.Branch})
	if err := apiErr(resp, err, func() int { return resp.StatusCode() }, func() *api.Error { return resp.JSONDefault }); err != nil {
		return res, fmt.Errorf("branch %s is ready, but forge could not record the start: %w", res.Branch, err)
	}
	return res, nil
}

func (f *Flow) checkoutIssueBranch(ctx context.Context, ws workspace, opts StartOptions, issue *github.Issue) (string, bool, error) {
	n := opts.Issue.Number
	local, err := ws.git.localBranches(ctx)
	if err != nil {
		return "", false, err
	}
	if b := findIssueBranch(local, n); b != "" {
		_, err := ws.git.run(ctx, "switch", b)
		return b, false, err
	}
	remote, err := ws.git.remoteBranches(ctx)
	if err != nil {
		return "", false, err
	}
	if b := findIssueBranch(remote, n); b != "" {
		// Started on another machine: track it.
		if _, err := ws.git.run(ctx, "fetch", "origin", b); err != nil {
			return "", false, err
		}
		_, err := ws.git.run(ctx, "switch", "--track", "-c", b, "origin/"+b)
		return b, false, err
	}

	typ := opts.Type
	if typ == "" {
		var labels []string
		for _, l := range issue.Labels {
			labels = append(labels, l.GetName())
		}
		typ = domain.BranchTypeFromLabels(labels)
	}
	name := domain.BranchName(typ, n, issue.GetTitle())
	base, err := f.defaultBranch(ctx, ws)
	if err != nil {
		return "", false, err
	}
	if _, err := ws.git.run(ctx, "fetch", "origin", base); err != nil {
		return "", false, err
	}
	if _, err := ws.git.run(ctx, "switch", "--no-track", "-c", name, "origin/"+base); err != nil {
		return "", false, err
	}
	return name, true, nil
}

func (f *Flow) defaultBranch(ctx context.Context, ws workspace) (string, error) {
	r, _, err := ws.gh.Repositories.Get(ctx, ws.repo.Owner, ws.repo.Name)
	if err != nil {
		return "", fmt.Errorf("get repository %s: %w", ws.repo, err)
	}
	return r.GetDefaultBranch(), nil
}

// OpenPR pushes the branch, opens (or finds) its pull request linked to the
// issue and records it in forge, which moves the Project status to review.
func (f *Flow) OpenPR(ctx context.Context, opts PROptions) (PRResult, error) {
	ws, err := f.open(ctx, opts.RepoPath)
	if err != nil {
		return PRResult{}, err
	}
	branch, err := ws.git.currentBranch(ctx)
	if err != nil {
		return PRResult{}, err
	}
	var ref domain.IssueRef
	if opts.Issue != nil {
		ref = *opts.Issue
	} else {
		b, ok := domain.ParseBranch(branch)
		if !ok {
			return PRResult{}, fmt.Errorf("branch %q does not follow <type>/<issue>-<slug>; pass the issue explicitly", branch)
		}
		ref = domain.IssueRef{Repo: ws.repo, Number: b.Issue}
	}
	base, err := f.defaultBranch(ctx, ws)
	if err != nil {
		return PRResult{}, err
	}
	if branch == base {
		return PRResult{}, fmt.Errorf("you are on %s; run `forge start` first", base)
	}

	progress(opts.Progress, "Subiendo %s", branch)
	if _, err := ws.git.run(ctx, "push", "-u", "origin", branch); err != nil {
		return PRResult{}, err
	}

	var res PRResult
	prs, _, err := ws.gh.PullRequests.List(ctx, ws.repo.Owner, ws.repo.Name, &github.PullRequestListOptions{
		State: "open", Head: ws.repo.Owner + ":" + branch,
	})
	if err != nil {
		return PRResult{}, fmt.Errorf("list pull requests: %w", err)
	}
	if len(prs) > 0 {
		res = PRResult{Number: prs[0].GetNumber(), URL: prs[0].GetHTMLURL(), Existing: true}
	} else {
		title := opts.Title
		if title == "" {
			issue, _, err := ws.gh.Issues.Get(ctx, ref.Repo.Owner, ref.Repo.Name, ref.Number)
			if err != nil {
				return PRResult{}, fmt.Errorf("get issue %s: %w", ref, err)
			}
			title = issue.GetTitle()
		}
		progress(opts.Progress, "Abriendo pull request")
		pr, _, err := ws.gh.PullRequests.Create(ctx, ws.repo.Owner, ws.repo.Name, github.CreatePullRequest{
			Title: new(title),
			Head:  branch,
			Base:  base,
			Body:  new(prBody(opts.Body, ref, ws.repo)),
			Draft: new(opts.Draft),
		})
		if err != nil {
			return PRResult{}, fmt.Errorf("create pull request: %w", err)
		}
		res = PRResult{Number: pr.GetNumber(), URL: pr.GetHTMLURL()}
	}

	progress(opts.Progress, "Registrando PR en forge")
	resp, err := f.api.RecordTaskPRWithResponse(ctx, ref.Repo.Owner, ref.Repo.Name, ref.Number, api.RecordPRRequest{PrNumber: res.Number})
	if err := apiErr(resp, err, func() int { return resp.StatusCode() }, func() *api.Error { return resp.JSONDefault }); err != nil {
		return res, fmt.Errorf("PR #%d is open, but forge could not record it: %w", res.Number, err)
	}
	return res, nil
}

func prBody(body string, ref domain.IssueRef, repo domain.RepoRef) string {
	closes := fmt.Sprintf("Closes #%d", ref.Number)
	if !ref.Repo.Equal(repo) {
		closes = "Closes " + ref.String()
	}
	if body == "" {
		return closes
	}
	return body + "\n\n" + closes
}

func assigned(issue *github.Issue, login string) bool {
	for _, a := range issue.Assignees {
		if a.GetLogin() == login {
			return true
		}
	}
	return false
}

// apiErr turns a generated-client response into an error.
func apiErr[T any](resp *T, err error, status func() int, body func() *api.Error) error {
	if err != nil {
		return err
	}
	if s := status(); s >= 300 {
		if e := body(); e != nil {
			return errors.New(e.Message)
		}
		return errors.New(http.StatusText(s))
	}
	return nil
}
