// Package github implements app.GitHub with a GitHub App installation:
// REST through go-github and Projects v2 through a small GraphQL helper.
package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	gh "github.com/google/go-github/v92/github"

	"github.com/hiberus-industria/forge/internal/app"
	"github.com/hiberus-industria/forge/internal/domain"
)

const defaultBaseURL = "https://api.github.com/"

// Config configures the GitHub App installation.
type Config struct {
	Org            string
	AppID          int64
	InstallationID int64
	PrivateKeyPEM  []byte
	// BaseURL overrides the REST API root (tests, GHES), e.g. https://ghe.example.com/api/v3/.
	BaseURL string
}

// Client is the organization-level GitHub integration.
type Client struct {
	org     string
	baseURL string
	rest    *gh.Client
	gql     *graphQL
}

var _ app.GitHub = (*Client)(nil)

// New authenticates as the App installation. Tokens are minted and refreshed
// transparently by ghinstallation.
func New(cfg Config) (*Client, error) {
	if cfg.Org == "" || cfg.AppID == 0 || cfg.InstallationID == 0 || len(cfg.PrivateKeyPEM) == 0 {
		return nil, errors.New("github: org, app id, installation id and private key are required")
	}
	base := cfg.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	tr, err := ghinstallation.New(http.DefaultTransport, cfg.AppID, cfg.InstallationID, cfg.PrivateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("github: app credentials: %w", err)
	}
	tr.BaseURL = strings.TrimSuffix(base, "/")
	hc := &http.Client{Transport: tr, Timeout: 30 * time.Second}
	rest, err := gh.NewClient(gh.WithHTTPClient(hc), gh.WithURLs(&base, &base))
	if err != nil {
		return nil, err
	}
	return &Client{org: cfg.Org, baseURL: base, rest: rest, gql: &graphQL{hc: hc, url: graphQLURL(base)}}, nil
}

// graphQLURL derives the GraphQL endpoint from the REST root
// (api.github.com/ → api.github.com/graphql, GHES /api/v3/ → /api/graphql).
func graphQLURL(base string) string {
	b := strings.TrimSuffix(base, "/")
	if strings.HasSuffix(b, "/api/v3") {
		return strings.TrimSuffix(b, "/v3") + "/graphql"
	}
	return b + "/graphql"
}

func (c *Client) Org() string { return c.org }

func (c *Client) ListRepos(ctx context.Context) ([]app.RepoInfo, error) {
	var out []app.RepoInfo
	opts := &gh.ListOptions{PerPage: 100}
	for {
		page, resp, err := c.rest.Apps.ListRepos(ctx, opts)
		if err != nil {
			return nil, wrap("list installation repositories", err)
		}
		for _, r := range page.Repositories {
			if !strings.EqualFold(r.GetOwner().GetLogin(), c.org) {
				continue
			}
			out = append(out, app.RepoInfo{
				Ref:           domain.RepoRef{Owner: r.GetOwner().GetLogin(), Name: r.GetName()},
				NodeID:        r.GetNodeID(),
				DefaultBranch: r.GetDefaultBranch(),
				Archived:      r.GetArchived(),
			})
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

func (c *Client) GetIssue(ctx context.Context, ref domain.IssueRef) (app.IssueInfo, error) {
	is, _, err := c.rest.Issues.Get(ctx, ref.Repo.Owner, ref.Repo.Name, ref.Number)
	if err != nil {
		return app.IssueInfo{}, wrap("get issue "+ref.String(), err)
	}
	if is.IsPullRequest() {
		return app.IssueInfo{}, fmt.Errorf("%s is a pull request: %w", ref, app.ErrNotFound)
	}
	return issueInfo(ref.Repo, is), nil
}

func (c *Client) ListIssuesUpdatedSince(ctx context.Context, repo domain.RepoRef, since time.Time) ([]app.IssueInfo, error) {
	opts := &gh.IssueListByRepoOptions{
		State:       "open",
		Sort:        "updated",
		Direction:   "desc",
		ListOptions: gh.ListOptions{PerPage: 100},
	}
	if !since.IsZero() {
		opts.State = "all"
		opts.Since = since
	}
	var out []app.IssueInfo
	for {
		page, resp, err := c.rest.Issues.ListByRepo(ctx, repo.Owner, repo.Name, opts)
		if err != nil {
			return nil, wrap("list issues of "+repo.String(), err)
		}
		for _, is := range page {
			if is.IsPullRequest() {
				continue
			}
			out = append(out, issueInfo(repo, is))
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opts.ListOptions.Page = resp.NextPage
	}
}

func (c *Client) UserFromToken(ctx context.Context, userToken string) (string, error) {
	if userToken == "" {
		return "", fmt.Errorf("empty github token: %w", app.ErrInvalid)
	}
	uc, err := gh.NewClient(gh.WithAuthToken(userToken), gh.WithURLs(&c.baseURL, &c.baseURL), gh.WithTimeout(15*time.Second))
	if err != nil {
		return "", err
	}
	u, _, err := uc.Users.Get(ctx, "")
	if err != nil {
		var er *gh.ErrorResponse
		if errors.As(err, &er) && er.Response != nil && er.Response.StatusCode == http.StatusUnauthorized {
			return "", fmt.Errorf("github token rejected: %w", app.ErrUnauthorized)
		}
		return "", wrap("resolve token owner", err)
	}
	return u.GetLogin(), nil
}

func (c *Client) IsOrgMember(ctx context.Context, login string) (bool, error) {
	ok, _, err := c.rest.Organizations.IsMember(ctx, c.org, login)
	if err != nil {
		return false, wrap("check membership of "+login, err)
	}
	return ok, nil
}

func issueInfo(repo domain.RepoRef, is *gh.Issue) app.IssueInfo {
	info := app.IssueInfo{
		Ref:       domain.IssueRef{Repo: repo, Number: is.GetNumber()},
		NodeID:    is.GetNodeID(),
		Title:     is.GetTitle(),
		URL:       is.GetHTMLURL(),
		State:     app.IssueState(is.GetState()),
		UpdatedAt: is.GetUpdatedAt().Time,
	}
	for _, a := range is.Assignees {
		info.Assignees = append(info.Assignees, a.GetLogin())
	}
	for _, l := range is.Labels {
		info.Labels = append(info.Labels, l.GetName())
	}
	if is.ClosedAt != nil {
		t := is.ClosedAt.Time
		info.ClosedAt = &t
	}
	return info
}

// wrap maps GitHub 404s to app.ErrNotFound and 403s to app.ErrForbidden.
func wrap(what string, err error) error {
	var er *gh.ErrorResponse
	if errors.As(err, &er) && er.Response != nil {
		switch er.Response.StatusCode {
		case http.StatusNotFound:
			return fmt.Errorf("github: %s: %w", what, app.ErrNotFound)
		case http.StatusForbidden:
			return fmt.Errorf("github: %s: %w: %w", what, app.ErrForbidden, err)
		}
	}
	return fmt.Errorf("github: %s: %w", what, err)
}
