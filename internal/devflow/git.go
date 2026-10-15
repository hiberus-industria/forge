package devflow

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/hiberus-industria/forge/internal/domain"
)

// git runs git commands in a working tree.
type git struct{ dir string }

func (g git) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", g.dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(out.String()), nil
}

func (g git) topLevel(ctx context.Context) (string, error) {
	top, err := g.run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%s is not inside a git repository", g.dir)
	}
	return top, nil
}

// originRepo reads the raw origin URL (not rewritten by insteadOf) and
// returns the GitHub repository it points to.
func (g git) originRepo(ctx context.Context) (domain.RepoRef, error) {
	u, err := g.run(ctx, "config", "--get", "remote.origin.url")
	if err != nil {
		return domain.RepoRef{}, fmt.Errorf("no origin remote: %w", err)
	}
	return ParseRemote(u)
}

func (g git) currentBranch(ctx context.Context) (string, error) {
	b, err := g.run(ctx, "branch", "--show-current")
	if err == nil && b == "" {
		return "", fmt.Errorf("detached HEAD: check out a branch first")
	}
	return b, err
}

func (g git) localBranches(ctx context.Context) ([]string, error) {
	out, err := g.run(ctx, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

func (g git) remoteBranches(ctx context.Context) ([]string, error) {
	out, err := g.run(ctx, "ls-remote", "--heads", "origin")
	if err != nil || out == "" {
		return nil, err
	}
	var names []string
	for _, l := range strings.Split(out, "\n") {
		if _, ref, ok := strings.Cut(l, "\t"); ok {
			names = append(names, strings.TrimPrefix(ref, "refs/heads/"))
		}
	}
	return names, nil
}

// remoteRe matches the GitHub remote forms: git@github.com:o/n.git,
// ssh://git@github.com/o/n.git, https://github.com/o/n(.git), with optional
// credentials or port.
var remoteRe = regexp.MustCompile(`^(?:[a-z+]+://)?(?:[^@/]+@)?[^:/]+(?::\d+)?[:/]([^/]+)/([^/]+?)(?:\.git)?/?$`)

// ParseRemote extracts owner/name from a git remote URL.
func ParseRemote(url string) (domain.RepoRef, error) {
	m := remoteRe.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return domain.RepoRef{}, fmt.Errorf("cannot parse GitHub repository from remote %q", url)
	}
	return domain.RepoRef{Owner: m[1], Name: m[2]}, nil
}

// findIssueBranch returns the first branch following the convention for issue.
func findIssueBranch(branches []string, issue int) string {
	for _, b := range branches {
		if p, ok := domain.ParseBranch(b); ok && p.Issue == issue {
			return b
		}
	}
	return ""
}
