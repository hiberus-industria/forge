package github

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	gh "github.com/google/go-github/v92/github"

	"github.com/hiberus-industria/forge/internal/app"
	"github.com/hiberus-industria/forge/internal/domain"
)

// WebhookParser verifies and translates GitHub App webhook deliveries.
type WebhookParser struct {
	secret []byte
	org    string
	now    func() time.Time
}

var _ app.WebhookParser = (*WebhookParser)(nil)

// NewWebhookParser validates X-Hub-Signature-256 with secret and ignores
// deliveries from organizations other than org.
func NewWebhookParser(secret []byte, org string) app.WebhookParser {
	return &WebhookParser{secret: secret, org: org, now: time.Now}
}

func (p *WebhookParser) Parse(r *http.Request) ([]app.GitHubEvent, error) {
	payload, err := gh.ValidatePayload(r, p.secret)
	if err != nil {
		return nil, fmt.Errorf("github webhook: %w: %w", app.ErrUnauthorized, err)
	}
	ev, err := gh.ParseWebHook(gh.WebHookType(r), payload)
	if err != nil {
		// Unknown event types are irrelevant, not errors.
		return nil, nil
	}
	switch e := ev.(type) {
	case *gh.IssuesEvent:
		return p.issues(e), nil
	case *gh.PullRequestEvent:
		return p.pullRequest(e), nil
	case *gh.RepositoryEvent:
		if !p.ours(e.GetRepo().GetOwner().GetLogin()) {
			return nil, nil
		}
		return []app.GitHubEvent{{Kind: app.GHRepoChanged, Repo: repoRef(e.GetRepo()), At: p.now()}}, nil
	case *gh.InstallationRepositoriesEvent:
		var out []app.GitHubEvent
		for _, r := range append(append([]*gh.Repository{}, e.RepositoriesAdded...), e.RepositoriesRemoved...) {
			ref, ok := p.repoFromFullName(r)
			if ok {
				out = append(out, app.GitHubEvent{Kind: app.GHRepoChanged, Repo: ref, At: p.now()})
			}
		}
		return out, nil
	case *gh.ProjectV2ItemEvent:
		// The payload does not say which repository the item belongs to: an
		// empty repo name is an organization-wide resync hint.
		if !p.ours(e.GetOrg().GetLogin()) {
			return nil, nil
		}
		return []app.GitHubEvent{{Kind: app.GHRepoChanged, Repo: domain.RepoRef{Owner: e.GetOrg().GetLogin()}, At: p.now()}}, nil
	}
	return nil, nil
}

func (p *WebhookParser) ours(owner string) bool { return strings.EqualFold(owner, p.org) }

func (p *WebhookParser) repoFromFullName(r *gh.Repository) (domain.RepoRef, bool) {
	ref, err := domain.ParseRepoRef(r.GetFullName())
	if err != nil || !p.ours(ref.Owner) {
		return domain.RepoRef{}, false
	}
	return ref, true
}

func (p *WebhookParser) issues(e *gh.IssuesEvent) []app.GitHubEvent {
	repo := repoRef(e.GetRepo())
	if !p.ours(repo.Owner) || e.Issue == nil || e.Issue.IsPullRequest() {
		return nil
	}
	var kind app.GitHubEventKind
	switch e.GetAction() {
	case "opened", "edited", "labeled", "unlabeled", "assigned", "unassigned", "transferred":
		kind = app.GHIssueUpserted
	case "closed":
		kind = app.GHIssueClosed
	case "reopened":
		kind = app.GHIssueReopened
	default:
		return nil
	}
	info := issueInfo(repo, e.Issue)
	at := info.UpdatedAt
	if at.IsZero() {
		at = p.now()
	}
	return []app.GitHubEvent{{Kind: kind, Repo: repo, Issue: &info, At: at}}
}

func (p *WebhookParser) pullRequest(e *gh.PullRequestEvent) []app.GitHubEvent {
	repo := repoRef(e.GetRepo())
	pr := e.GetPullRequest()
	if !p.ours(repo.Owner) || pr == nil {
		return nil
	}
	var kind app.GitHubEventKind
	switch a := e.GetAction(); {
	case a == "opened" || a == "reopened" || a == "ready_for_review":
		kind = app.GHPROpened
	case a == "closed" && pr.GetMerged():
		kind = app.GHPRMerged
	default:
		return nil
	}
	at := pr.GetUpdatedAt().Time
	if kind == app.GHPRMerged && pr.MergedAt != nil {
		at = pr.MergedAt.Time
	}
	if at.IsZero() {
		at = p.now()
	}
	return []app.GitHubEvent{{
		Kind:         kind,
		Repo:         repo,
		PRNumber:     pr.GetNumber(),
		LinkedIssues: LinkedIssues(repo, pr.GetHead().GetRef(), pr.GetBody()),
		At:           at,
	}}
}

// closingRe matches GitHub closing keywords: "Closes #12", "fixes org/repo#3".
var closingRe = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s*:?\s+(?:([\w.-]+)/([\w.-]+))?#(\d+)\b`)

// LinkedIssues returns the issues of repo a pull request works on: the one in
// its head branch name (forge convention) plus those referenced with closing
// keywords in the body. References to other repositories are ignored.
func LinkedIssues(repo domain.RepoRef, headRef, body string) []int {
	var out []int
	seen := map[int]bool{}
	add := func(n int) {
		if n > 0 && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if b, ok := domain.ParseBranch(headRef); ok {
		add(b.Issue)
	}
	for _, m := range closingRe.FindAllStringSubmatch(body, -1) {
		if m[1] != "" && !repo.Equal(domain.RepoRef{Owner: m[1], Name: m[2]}) {
			continue
		}
		n, _ := strconv.Atoi(m[3])
		add(n)
	}
	return out
}

func repoRef(r *gh.Repository) domain.RepoRef {
	return domain.RepoRef{Owner: r.GetOwner().GetLogin(), Name: r.GetName()}
}
