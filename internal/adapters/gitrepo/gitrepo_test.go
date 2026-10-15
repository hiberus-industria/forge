package gitrepo

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestResolveRepoBranchAndWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	main := filepath.Join(root, "main")
	if err := os.MkdirAll(filepath.Join(main, "sub", "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, main, "init", "-q", "-b", "main")
	git(t, main, "remote", "add", "origin", "git@github.com:hiberus-industria/forge.git")
	git(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	git(t, main, "checkout", "-q", "-b", "feat/12-x")

	r := NewResolver()
	info, err := r.Resolve(filepath.Join(main, "sub", "dir"))
	if err != nil {
		t.Fatal(err)
	}
	if info.TopLevel != main || info.Branch != "feat/12-x" || info.Repo == nil || info.Repo.String() != "hiberus-industria/forge" {
		t.Fatalf("unexpected %+v", info)
	}

	// The branch is read fresh on every call.
	git(t, main, "checkout", "-q", "main")
	if info, _ := r.Resolve(main); info.Branch != "main" {
		t.Fatalf("branch not refreshed: %q", info.Branch)
	}

	wt := filepath.Join(root, "wt")
	git(t, main, "worktree", "add", "-q", "-b", "fix/7-y", wt)
	info, err = r.Resolve(wt)
	if err != nil {
		t.Fatal(err)
	}
	if info.TopLevel != wt || info.Branch != "fix/7-y" || info.Repo == nil || info.Repo.Name != "forge" {
		t.Fatalf("worktree: unexpected %+v", info)
	}

	git(t, main, "checkout", "-q", "--detach")
	if info, _ := r.Resolve(main); info.Branch != "" {
		t.Fatalf("detached head should have no branch, got %q", info.Branch)
	}

	if _, err := r.Resolve(root); !errors.Is(err, ErrNotARepo) {
		t.Fatalf("expected ErrNotARepo, got %v", err)
	}
}

func TestParseRemoteURL(t *testing.T) {
	for _, u := range []string{
		"git@github.com:hiberus-industria/forge.git",
		"ssh://git@github.com/hiberus-industria/forge.git",
		"https://github.com/hiberus-industria/forge",
		"https://user:tok@github.com/hiberus-industria/forge.git/",
	} {
		r, err := ParseRemoteURL(u)
		if err != nil || r.String() != "hiberus-industria/forge" {
			t.Errorf("%s: %v %v", u, r, err)
		}
	}
	if _, err := ParseRemoteURL("/local/path"); err == nil {
		t.Error("local path accepted")
	}
}
