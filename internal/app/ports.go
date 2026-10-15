// Package app holds the API server use cases. It orchestrates the domain and
// talks to the outside world only through the ports declared here and in
// store.go.
package app

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/hiberus-industria/forge/internal/domain"
)

// Principal is the authenticated caller, extracted from a Keycloak access token.
type Principal struct {
	Subject string // Keycloak "sub"
	Email   string
	Name    string
	Admin   bool // client role "admin" of the forge-api client
}

type principalKey struct{}

// WithPrincipal stores the caller in the context.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the caller stored in the context.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Application errors, mapped to HTTP status codes by the HTTP adapter.
var (
	ErrNotFound     = errors.New("not found")
	ErrForbidden    = errors.New("forbidden")
	ErrUnauthorized = errors.New("unauthorized")
	ErrInvalid      = errors.New("invalid request")
	ErrConflict     = errors.New("conflict")
)

// TokenVerifier validates bearer tokens (Keycloak JWT via JWKS).
type TokenVerifier interface {
	Verify(ctx context.Context, rawToken string) (Principal, error)
}

// ---------------------------------------------------------------- GitHub

// RepoInfo is an organization repository.
type RepoInfo struct {
	Ref           domain.RepoRef
	NodeID        string
	DefaultBranch string
	Archived      bool
}

// IssueState is the GitHub state of an issue.
type IssueState string

const (
	IssueOpen   IssueState = "open"
	IssueClosed IssueState = "closed"
)

// IssueInfo is a GitHub issue (pull requests excluded).
type IssueInfo struct {
	Ref       domain.IssueRef
	NodeID    string
	Title     string
	URL       string
	State     IssueState
	Assignees []string // GitHub logins
	Labels    []string
	UpdatedAt time.Time
	ClosedAt  *time.Time
}

// ProjectInfo is the GitHub Project (v2) linked to a repository, with the
// options of its single-select "Status" field.
type ProjectInfo struct {
	ID            string // node id
	Title         string
	URL           string
	StatusFieldID string
	Options       []domain.StatusOption
}

// GitHub is the organization-level integration, backed by the GitHub App.
// Actions done "as the developer" (branch, assignment, PR) happen on the
// developer machine with their own gh token and never go through this port.
type GitHub interface {
	// Org is the organization the App is installed on.
	Org() string
	ListRepos(ctx context.Context) ([]RepoInfo, error)
	GetIssue(ctx context.Context, ref domain.IssueRef) (IssueInfo, error)
	// ListIssuesUpdatedSince returns open and closed issues updated after since
	// (zero since = all open issues). Used by the periodic reconciliation.
	ListIssuesUpdatedSince(ctx context.Context, repo domain.RepoRef, since time.Time) ([]IssueInfo, error)
	// RepoProject returns the Project v2 linked to the repository, nil when none.
	// When several are linked, the first one (by title) with a Status field wins.
	RepoProject(ctx context.Context, repo domain.RepoRef) (*ProjectInfo, error)
	// SetProjectStatus adds the issue to the project if needed and sets its
	// Status to the option mapped for the state. Missing mapping is a no-op.
	SetProjectStatus(ctx context.Context, sm domain.StatusMap, issue IssueInfo, state domain.TaskState) error
	// UserFromToken resolves the login owning a user token (gh auth token).
	UserFromToken(ctx context.Context, userToken string) (login string, err error)
	IsOrgMember(ctx context.Context, login string) (bool, error)
}

// GitHubEventKind classifies webhook deliveries forge cares about.
type GitHubEventKind string

const (
	GHIssueUpserted GitHubEventKind = "issue_upserted" // opened, edited, labeled, assigned...
	GHIssueClosed   GitHubEventKind = "issue_closed"
	GHIssueReopened GitHubEventKind = "issue_reopened"
	GHPROpened      GitHubEventKind = "pr_opened" // opened, reopened or ready_for_review
	GHPRMerged      GitHubEventKind = "pr_merged"
	GHRepoChanged   GitHubEventKind = "repo_changed" // created, renamed, archived, project linked...
)

// GitHubEvent is a webhook delivery translated to forge's vocabulary.
type GitHubEvent struct {
	Kind GitHubEventKind
	Repo domain.RepoRef
	// Issue is set for issue events.
	Issue *IssueInfo
	// PRNumber and LinkedIssues are set for PR events. Linked issues come from
	// the head branch name (domain.ParseBranch) and closing keywords in the body.
	PRNumber     int
	LinkedIssues []int
	At           time.Time
}

// WebhookParser verifies (HMAC signature) and decodes GitHub webhooks.
// Irrelevant deliveries yield no events and no error.
type WebhookParser interface {
	Parse(r *http.Request) ([]GitHubEvent, error)
}

// ---------------------------------------------------------------- pricing & FX

// PriceSource provides seed prices (models.dev). Prices are USD per 1M tokens;
// EffectiveFrom is the fetch day; Source is PriceSourceModelsDev.
type PriceSource interface {
	FetchPrices(ctx context.Context) ([]domain.Price, error)
}

// FXSource provides ECB reference rates (EUR base) since a day, inclusive.
type FXSource interface {
	FetchRates(ctx context.Context, since time.Time) ([]domain.FXRate, error)
}
