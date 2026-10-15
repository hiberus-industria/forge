package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hiberus-industria/forge/internal/app"
	"github.com/hiberus-industria/forge/internal/domain"
)

const org = "hiberus-industria"

var repo = domain.RepoRef{Owner: org, Name: "api"}

type fakeGitHub struct {
	t         *testing.T
	srv       *httptest.Server
	mutations []map[string]any
}

func newFake(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app/installations/7/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("app JWT missing")
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"token":"inst-token","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
	})
	mux.HandleFunc("GET /installation/repositories", func(w http.ResponseWriter, r *http.Request) {
		f.requireInstallationToken(r)
		fmt.Fprint(w, `{"total_count":2,"repositories":[
			{"name":"api","node_id":"R1","default_branch":"main","archived":false,"owner":{"login":"hiberus-industria"}},
			{"name":"fork","node_id":"R2","default_branch":"master","owner":{"login":"someone-else"}}]}`)
	})
	mux.HandleFunc("GET /repos/hiberus-industria/api/issues/12", func(w http.ResponseWriter, r *http.Request) {
		f.requireInstallationToken(r)
		fmt.Fprint(w, `{"number":12,"node_id":"I12","title":"Login OIDC","html_url":"https://github.com/x/12","state":"open",
			"assignees":[{"login":"dev1"}],"labels":[{"name":"bug"}],"updated_at":"2026-10-01T10:00:00Z"}`)
	})
	mux.HandleFunc("GET /repos/hiberus-industria/api/issues/13", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"number":13,"node_id":"PR13","state":"open","pull_request":{"url":"x"}}`)
	})
	mux.HandleFunc("GET /repos/hiberus-industria/api/issues/404", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	})
	mux.HandleFunc("GET /repos/hiberus-industria/api/issues", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != "all" || q.Get("since") == "" {
			t.Errorf("unexpected query %s", r.URL.RawQuery)
		}
		if q.Get("page") == "" {
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/hiberus-industria/api/issues?page=2>; rel="next"`, f.srv.URL))
			fmt.Fprint(w, `[{"number":1,"state":"open","updated_at":"2026-10-02T00:00:00Z"},{"number":2,"pull_request":{"url":"x"}}]`)
			return
		}
		fmt.Fprint(w, `[{"number":3,"state":"closed","closed_at":"2026-10-03T00:00:00Z"}]`)
	})
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer user-token" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"Bad credentials"}`)
			return
		}
		fmt.Fprint(w, `{"login":"dev1"}`)
	})
	mux.HandleFunc("GET /orgs/hiberus-industria/members/dev1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /orgs/hiberus-industria/members/stranger", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("POST /graphql", f.graphql)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) requireInstallationToken(r *http.Request) {
	if got := r.Header.Get("Authorization"); got != "token inst-token" && got != "Bearer inst-token" {
		f.t.Errorf("installation token missing, got %q", got)
	}
}

func (f *fakeGitHub) graphql(w http.ResponseWriter, r *http.Request) {
	f.requireInstallationToken(r)
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	body, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(body, &req); err != nil {
		f.t.Fatal(err)
	}
	switch {
	case strings.Contains(req.Query, "projectsV2"):
		if req.Variables["name"] == "missing" {
			fmt.Fprint(w, `{"data":{"repository":null},"errors":[{"type":"NOT_FOUND","message":"Could not resolve to a Repository"}]}`)
			return
		}
		fmt.Fprint(w, `{"data":{"repository":{"projectsV2":{"nodes":[
			{"id":"PZ","title":"Zeta","url":"u","closed":false,"fields":{"nodes":[{"id":"FZ","name":"Status","options":[{"id":"z","name":"Todo"}]}]}},
			{"id":"PC","title":"Alpha closed","url":"u","closed":true,"fields":{"nodes":[{"id":"FC","name":"Status","options":[]}]}},
			{"id":"PN","title":"Beta no status","url":"u","closed":false,"fields":{"nodes":[{},{"id":"FX","name":"Priority","options":[]}]}},
			{"id":"PA","title":"Gamma","url":"https://github.com/orgs/x/projects/1","closed":false,"fields":{"nodes":[{},{"id":"FA","name":"status","options":[{"id":"o1","name":"Todo"},{"id":"o2","name":"In Progress"}]}]}}
		]}}}}`)
	case strings.Contains(req.Query, "addProjectV2ItemById"):
		f.mutations = append(f.mutations, req.Variables)
		fmt.Fprint(w, `{"data":{"addProjectV2ItemById":{"item":{"id":"ITEM1"}}}}`)
	case strings.Contains(req.Query, "updateProjectV2ItemFieldValue"):
		f.mutations = append(f.mutations, req.Variables)
		fmt.Fprint(w, `{"data":{"updateProjectV2ItemFieldValue":{"projectV2Item":{"id":"ITEM1"}}}}`)
	default:
		f.t.Errorf("unexpected query %s", req.Query)
	}
}

func testKey(t *testing.T) []byte {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
}

func newClient(t *testing.T, f *fakeGitHub) *Client {
	c, err := New(Config{Org: org, AppID: 1, InstallationID: 7, PrivateKeyPEM: testKey(t), BaseURL: f.srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewValidates(t *testing.T) {
	if _, err := New(Config{Org: org}); err == nil {
		t.Fatal("expected error for incomplete config")
	}
}

func TestGraphQLURL(t *testing.T) {
	cases := map[string]string{
		"https://api.github.com/":         "https://api.github.com/graphql",
		"https://ghe.example.com/api/v3/": "https://ghe.example.com/api/graphql",
		"http://127.0.0.1:1234/":          "http://127.0.0.1:1234/graphql",
	}
	for in, want := range cases {
		if got := graphQLURL(in); got != want {
			t.Errorf("graphQLURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRESTOperations(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f)
	ctx := context.Background()

	repos, err := c.ListRepos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Ref != repo || repos[0].DefaultBranch != "main" || repos[0].NodeID != "R1" {
		t.Fatalf("repos = %+v", repos)
	}

	is, err := c.GetIssue(ctx, domain.IssueRef{Repo: repo, Number: 12})
	if err != nil {
		t.Fatal(err)
	}
	if is.Title != "Login OIDC" || is.NodeID != "I12" || is.State != app.IssueOpen ||
		len(is.Assignees) != 1 || is.Assignees[0] != "dev1" || is.Labels[0] != "bug" {
		t.Fatalf("issue = %+v", is)
	}
	if _, err := c.GetIssue(ctx, domain.IssueRef{Repo: repo, Number: 13}); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("pull request should be not found, got %v", err)
	}
	if _, err := c.GetIssue(ctx, domain.IssueRef{Repo: repo, Number: 404}); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("404 should map to ErrNotFound, got %v", err)
	}

	list, err := c.ListIssuesUpdatedSince(ctx, repo, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Ref.Number != 1 || list[1].Ref.Number != 3 || list[1].ClosedAt == nil {
		t.Fatalf("list = %+v", list)
	}

	login, err := c.UserFromToken(ctx, "user-token")
	if err != nil || login != "dev1" {
		t.Fatalf("login = %q, %v", login, err)
	}
	if _, err := c.UserFromToken(ctx, "bad"); !errors.Is(err, app.ErrUnauthorized) {
		t.Fatalf("bad token should be unauthorized, got %v", err)
	}
	if ok, err := c.IsOrgMember(ctx, "dev1"); err != nil || !ok {
		t.Fatalf("dev1 member = %v, %v", ok, err)
	}
	if ok, err := c.IsOrgMember(ctx, "stranger"); err != nil || ok {
		t.Fatalf("stranger member = %v, %v", ok, err)
	}
}

func TestRepoProject(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f)
	p, err := c.RepoProject(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	// Sorted by title: "Alpha closed" (skipped), "Beta no status" (skipped), "Gamma" wins over "Zeta".
	if p == nil || p.ID != "PA" || p.StatusFieldID != "FA" || len(p.Options) != 2 || p.Options[1].Name != "In Progress" {
		t.Fatalf("project = %+v", p)
	}
	if _, err := c.RepoProject(context.Background(), domain.RepoRef{Owner: org, Name: "missing"}); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing repo should be not found, got %v", err)
	}
}

func TestSetProjectStatus(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f)
	sm := domain.StatusMap{ProjectID: "PA", StatusFieldID: "FA", Options: map[domain.TaskState]string{domain.TaskInProgress: "o2"}}
	issue := app.IssueInfo{Ref: domain.IssueRef{Repo: repo, Number: 12}} // no node id: fetched via REST

	if err := c.SetProjectStatus(context.Background(), sm, issue, domain.TaskInReview); err != nil || len(f.mutations) != 0 {
		t.Fatalf("unmapped state must be a no-op: %v %v", err, f.mutations)
	}
	if err := c.SetProjectStatus(context.Background(), sm, issue, domain.TaskInProgress); err != nil {
		t.Fatal(err)
	}
	if len(f.mutations) != 2 {
		t.Fatalf("mutations = %v", f.mutations)
	}
	if f.mutations[0]["content"] != "I12" || f.mutations[0]["project"] != "PA" {
		t.Fatalf("add item vars = %v", f.mutations[0])
	}
	if m := f.mutations[1]; m["item"] != "ITEM1" || m["field"] != "FA" || m["option"] != "o2" {
		t.Fatalf("update vars = %v", m)
	}
}
