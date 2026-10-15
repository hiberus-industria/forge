package postgres

import (
	"fmt"
	"strings"

	"github.com/hiberus-industria/forge/internal/domain"
)

// where accumulates SQL conditions with positional arguments.
type where struct {
	conds []string
	args  []any
}

// add appends a condition; every "?" is replaced by the next placeholder.
func (w *where) add(cond string, args ...any) {
	for _, a := range args {
		w.args = append(w.args, a)
		cond = strings.Replace(cond, "?", fmt.Sprintf("$%d", len(w.args)), 1)
	}
	w.conds = append(w.conds, cond)
}

func (w *where) sql() string {
	if len(w.conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.conds, " AND ")
}

// next returns the placeholder for an extra argument appended after the conditions.
func (w *where) next(a any) string {
	w.args = append(w.args, a)
	return fmt.Sprintf("$%d", len(w.args))
}

func repoKey(r domain.RepoRef) string { return strings.ToLower(r.String()) }

func parseRepoKey(s string) *domain.RepoRef {
	if s == "" {
		return nil
	}
	r, err := domain.ParseRepoRef(s)
	if err != nil {
		return nil
	}
	return &r
}
