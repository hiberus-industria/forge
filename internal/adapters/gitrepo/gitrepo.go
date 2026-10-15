// Package gitrepo resolves the GitHub repository and the checked-out branch
// of a working directory by reading git metadata directly (no git binary),
// so it is cheap enough to run on every captured request.
package gitrepo

import (
	"bufio"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hiberus-industria/forge/internal/domain"
)

// ErrNotARepo means the directory is not inside a git working tree.
var ErrNotARepo = errors.New("not a git repository")

// Info describes the repository state of a directory.
type Info struct {
	// TopLevel is the working tree root (the worktree root for linked worktrees).
	TopLevel string
	// Repo is nil when no GitHub-like remote is configured.
	Repo *domain.RepoRef
	// Branch is empty on a detached HEAD.
	Branch string
}

// Resolver caches remote lookups per git common dir. HEAD is always read
// fresh because the branch may change at any moment.
type Resolver struct {
	mu      sync.Mutex
	remotes map[string]remoteEntry
}

type remoteEntry struct {
	modTime time.Time
	repo    *domain.RepoRef
}

// NewResolver returns an empty resolver.
func NewResolver() *Resolver { return &Resolver{remotes: map[string]remoteEntry{}} }

// Resolve inspects dir and its parents.
func (r *Resolver) Resolve(dir string) (Info, error) {
	top, gitDir, err := findGitDir(dir)
	if err != nil {
		return Info{}, err
	}
	info := Info{TopLevel: top, Branch: readBranch(gitDir)}
	info.Repo = r.remote(commonDir(gitDir))
	return info, nil
}

// findGitDir walks up from dir to the first directory holding .git (a
// directory, or a file "gitdir: <path>" for worktrees and submodules).
func findGitDir(dir string) (top, gitDir string, err error) {
	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	for {
		p := filepath.Join(dir, ".git")
		st, err := os.Stat(p)
		if err == nil {
			if st.IsDir() {
				return dir, p, nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return "", "", err
			}
			gd, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
			if !ok {
				return "", "", ErrNotARepo
			}
			gd = strings.TrimSpace(gd)
			if !filepath.IsAbs(gd) {
				gd = filepath.Join(dir, gd)
			}
			return dir, filepath.Clean(gd), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", ErrNotARepo
		}
		dir = parent
	}
}

func readBranch(gitDir string) string {
	b, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	ref, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "ref:")
	if !ok {
		return "" // detached
	}
	return strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/")
}

// commonDir returns the directory holding the shared config (differs from
// gitDir for linked worktrees).
func commonDir(gitDir string) string {
	b, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return gitDir
	}
	cd := strings.TrimSpace(string(b))
	if !filepath.IsAbs(cd) {
		cd = filepath.Join(gitDir, cd)
	}
	return filepath.Clean(cd)
}

func (r *Resolver) remote(common string) *domain.RepoRef {
	cfgPath := filepath.Join(common, "config")
	st, err := os.Stat(cfgPath)
	if err != nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.remotes[common]; ok && e.modTime.Equal(st.ModTime()) {
		return e.repo
	}
	repo := parseConfigRemote(cfgPath)
	r.remotes[common] = remoteEntry{modTime: st.ModTime(), repo: repo}
	return repo
}

// parseConfigRemote reads the url of remote "origin", or of the first remote.
func parseConfigRemote(path string) *domain.RepoRef {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var section, first, origin string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			section = strings.Trim(line, "[]")
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "url" || !strings.HasPrefix(section, "remote ") {
			continue
		}
		val = strings.TrimSpace(val)
		if first == "" {
			first = val
		}
		if section == `remote "origin"` {
			origin = val
		}
	}
	u := origin
	if u == "" {
		u = first
	}
	if u == "" {
		return nil
	}
	ref, err := ParseRemoteURL(u)
	if err != nil {
		return nil
	}
	return &ref
}

// ParseRemoteURL extracts owner/name from ssh, scp-like and https remotes.
func ParseRemoteURL(raw string) (domain.RepoRef, error) {
	raw = strings.Trim(strings.TrimSpace(raw), `"`)
	var path string
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return domain.RepoRef{}, err
		}
		path = u.Path
	} else if _, after, ok := strings.Cut(raw, ":"); ok { // git@github.com:owner/name.git
		path = after
	} else {
		return domain.RepoRef{}, errors.New("unsupported remote url")
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return domain.RepoRef{}, errors.New("remote url without owner/name")
	}
	owner, name := parts[len(parts)-2], parts[len(parts)-1]
	if owner == "" || name == "" {
		return domain.RepoRef{}, errors.New("remote url without owner/name")
	}
	return domain.RepoRef{Owner: owner, Name: name}, nil
}
