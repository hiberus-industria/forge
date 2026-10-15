package devflow

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
)

// ErrNoGitHubToken means neither GH_TOKEN/GITHUB_TOKEN nor `gh auth` is set up.
var ErrNoGitHubToken = errors.New("no GitHub token: run `gh auth login` (or set GH_TOKEN)")

// GitHubToken returns the developer's own GitHub token: GH_TOKEN,
// GITHUB_TOKEN, or `gh auth token`. Actions done with it are authored by the
// developer in GitHub.
func GitHubToken(ctx context.Context) (string, error) {
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := os.Getenv(k); v != "" {
			return v, nil
		}
	}
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if err != nil {
		return "", ErrNoGitHubToken
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", ErrNoGitHubToken
	}
	return tok, nil
}
