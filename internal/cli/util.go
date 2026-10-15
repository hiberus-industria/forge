package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/hiberus-industria/forge/internal/devflow"
	"github.com/hiberus-industria/forge/internal/domain"
)

func httpStatus(code int) string { return fmt.Sprintf("%d %s", code, http.StatusText(code)) }

// localRepo returns the git top-level and GitHub repository of the cwd.
func localRepo(ctx context.Context) (top string, repo domain.RepoRef, err error) {
	t, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", repo, fmt.Errorf("not inside a git repository")
	}
	u, err := exec.CommandContext(ctx, "git", "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return "", repo, fmt.Errorf("the repository has no origin remote")
	}
	repo, err = devflow.ParseRemote(string(u))
	return strings.TrimSpace(string(t)), repo, err
}

// parseIssueArg accepts 123, #123, repo#123 (in the configured org) and
// owner/repo#123. Bare numbers refer to the current clone's repository.
func parseIssueArg(ctx context.Context, org, arg string) (domain.IssueRef, error) {
	arg = strings.TrimSpace(arg)
	if n, err := strconv.Atoi(strings.TrimPrefix(arg, "#")); err == nil {
		if n <= 0 {
			return domain.IssueRef{}, fmt.Errorf("invalid issue number %q", arg)
		}
		_, repo, err := localRepo(ctx)
		if err != nil {
			return domain.IssueRef{}, fmt.Errorf("%w; use owner/repo#%d", err, n)
		}
		return domain.IssueRef{Repo: repo, Number: n}, nil
	}
	if r, num, ok := strings.Cut(arg, "#"); ok && !strings.Contains(r, "/") {
		arg = org + "/" + r + "#" + num
	}
	return domain.ParseIssueRef(arg)
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "hace un momento"
	case d < time.Hour:
		return fmt.Sprintf("hace %d min", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("hace %d h", int(d.Hours()))
	default:
		return fmt.Sprintf("hace %d días", int(d.Hours()/24))
	}
}

func isTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
