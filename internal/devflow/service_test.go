package devflow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/domain"
)

func TestParseRemote(t *testing.T) {
	for _, u := range []string{
		"git@github.com:hiberus-industria/forge.git",
		"https://github.com/hiberus-industria/forge",
		"https://github.com/hiberus-industria/forge.git",
		"ssh://git@github.com:22/hiberus-industria/forge.git",
		"https://x-access-token:abc@github.com/hiberus-industria/forge.git",
	} {
		r, err := ParseRemote(u)
		if err != nil || r.String() != "hiberus-industria/forge" {
			t.Errorf("%s -> %v %v", u, r, err)
		}
	}
}

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// fixture: bare "origin" reachable through a github.com URL rewritten with insteadOf.
func newClone(t *testing.T) string {
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	clone := filepath.Join(root, "clone")
	sh(t, root, "git", "init", "-q", "--bare", "-b", "main", bare)
	sh(t, root, "git", "init", "-q", "-b", "main", clone)
	sh(t, clone, "git", "config", "url."+bare+".insteadOf", "https://github.com/hiberus-industria/demo.git")
	sh(t, clone, "git", "remote", "add", "origin", "https://github.com/hiberus-industria/demo.git")
	sh(t, clone, "git", "commit", "-q", "--allow-empty", "-m", "init")
	sh(t, clone, "git", "push", "-q", "origin", "main")
	return clone
}

type recorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *recorder) add(s string) { r.mu.Lock(); r.calls = append(r.calls, s); r.mu.Unlock() }

func fakes(t *testing.T, rec *recorder) (gh, forge *httptest.Server) {
	g := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	g.HandleFunc("GET /api/v3/repos/hiberus-industria/demo/issues/7", func(w http.ResponseWriter, _ *http.Request) {
		write(w, map[string]any{"number": 7, "title": "Login con Keycloak", "state": "open", "labels": []any{map[string]any{"name": "bug"}}})
	})
	g.HandleFunc("GET /api/v3/repos/hiberus-industria/demo", func(w http.ResponseWriter, _ *http.Request) {
		write(w, map[string]any{"default_branch": "main"})
	})
	g.HandleFunc("GET /api/v3/user", func(w http.ResponseWriter, _ *http.Request) { write(w, map[string]any{"login": "dev"}) })
	g.HandleFunc("POST /api/v3/repos/hiberus-industria/demo/issues/7/assignees", func(w http.ResponseWriter, _ *http.Request) {
		rec.add("assign")
		write(w, map[string]any{"number": 7})
	})
	g.HandleFunc("GET /api/v3/repos/hiberus-industria/demo/pulls", func(w http.ResponseWriter, r *http.Request) {
		rec.add("list-prs " + r.URL.Query().Get("head"))
		write(w, []any{})
	})
	g.HandleFunc("POST /api/v3/repos/hiberus-industria/demo/pulls", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		rec.add("create-pr " + body["head"].(string) + " " + body["body"].(string))
		write(w, map[string]any{"number": 12, "html_url": "https://github.com/hiberus-industria/demo/pull/12"})
	})
	f := http.NewServeMux()
	task := map[string]any{
		"ref": map[string]any{"repo": "hiberus-industria/demo", "number": 7}, "title": "x", "url": "u", "state": "in_progress",
		"assignees": []string{}, "labels": []string{}, "cost_usd_micros": 0, "events": 0,
		"tokens": map[string]int{"input": 0, "output": 0, "reasoning": 0, "cache_read": 0, "cache_write": 0},
	}
	f.HandleFunc("POST /v1/tasks/hiberus-industria/demo/7/start", func(w http.ResponseWriter, r *http.Request) {
		var b api.StartTaskRequest
		json.NewDecoder(r.Body).Decode(&b)
		rec.add("api-start " + b.Branch)
		write(w, task)
	})
	f.HandleFunc("POST /v1/tasks/hiberus-industria/demo/7/pr", func(w http.ResponseWriter, r *http.Request) {
		var b api.RecordPRRequest
		json.NewDecoder(r.Body).Decode(&b)
		rec.add("api-pr")
		write(w, task)
	})
	gh, forge = httptest.NewServer(g), httptest.NewServer(f)
	t.Cleanup(gh.Close)
	t.Cleanup(forge.Close)
	return gh, forge
}

func TestStartAndOpenPR(t *testing.T) {
	clone := newClone(t)
	rec := &recorder{}
	gh, forge := fakes(t, rec)
	client, err := api.NewClientWithResponses(forge.URL)
	if err != nil {
		t.Fatal(err)
	}
	flow := New(client, WithGitHubBaseURL(gh.URL+"/"), WithTokenFunc(func(context.Context) (string, error) { return "tok", nil }))
	ctx := context.Background()
	ref := domain.IssueRef{Repo: domain.RepoRef{Owner: "hiberus-industria", Name: "demo"}, Number: 7}

	var steps []string
	res, err := flow.Start(ctx, StartOptions{Issue: ref, RepoPath: clone, Progress: func(s string) { steps = append(steps, s) }})
	if err != nil {
		t.Fatal(err)
	}
	if res.Branch != "fix/7-login-con-keycloak" || !res.Created {
		t.Fatalf("unexpected %+v", res)
	}
	if got := sh(t, clone, "git", "branch", "--show-current"); got != res.Branch {
		t.Fatalf("checked out %q", got)
	}
	if len(steps) == 0 {
		t.Fatal("no progress reported")
	}
	// Idempotent: second start reuses the branch.
	if res2, err := flow.Start(ctx, StartOptions{Issue: ref, RepoPath: clone}); err != nil || res2.Created {
		t.Fatalf("restart: %+v %v", res2, err)
	}

	sh(t, clone, "git", "commit", "-q", "--allow-empty", "-m", "work")
	pr, err := flow.OpenPR(ctx, PROptions{RepoPath: clone, Body: "Resumen"})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 12 || pr.Existing {
		t.Fatalf("unexpected %+v", pr)
	}
	joined := strings.Join(rec.calls, "\n")
	for _, want := range []string{
		"assign", "api-start fix/7-login-con-keycloak", "list-prs hiberus-industria:fix/7-login-con-keycloak",
		"create-pr fix/7-login-con-keycloak Resumen\n\nCloses #7", "api-pr",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing call %q in:\n%s", want, joined)
		}
	}
	if out := sh(t, clone, "git", "ls-remote", "--heads", "origin"); !strings.Contains(out, "fix/7-login-con-keycloak") {
		t.Fatal("branch not pushed")
	}
}

func TestStartRejectsForeignRepo(t *testing.T) {
	clone := newClone(t)
	flow := New(nil, WithTokenFunc(func(context.Context) (string, error) { return "tok", nil }))
	_, err := flow.Start(context.Background(), StartOptions{
		Issue: domain.IssueRef{Repo: domain.RepoRef{Owner: "hiberus-industria", Name: "other"}, Number: 1}, RepoPath: clone,
	})
	if err == nil || !strings.Contains(err.Error(), "belongs to") {
		t.Fatalf("got %v", err)
	}
}
