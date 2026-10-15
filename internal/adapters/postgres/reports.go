package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/hiberus-industria/forge/internal/app"
	"github.com/hiberus-industria/forge/internal/domain"
)

// groupExprs returns the key and label SQL expressions of a dimension.
func groupExprs(g app.GroupBy) (key, label string, err error) {
	switch g {
	case app.GroupRepo:
		return `coalesce(e.repo, '(external)')`, `coalesce(e.repo, 'personal / other')`, nil
	case app.GroupIssue:
		return `CASE WHEN e.issue_number IS NULL THEN '(unassigned)' ELSE e.repo || '#' || e.issue_number END`,
			`CASE WHEN e.issue_number IS NULL THEN 'unassigned'
			      ELSE e.repo || '#' || e.issue_number || coalesce(nullif(' ' || i.title, ' '), '') END`, nil
	case app.GroupUser:
		return `u.id::text`, `coalesce(nullif(u.name, ''), u.github_login, u.email)`, nil
	case app.GroupModel:
		return `e.model`, `e.model`, nil
	case app.GroupHarness:
		return `e.harness`, `e.harness`, nil
	case app.GroupDay:
		return `to_char(e.occurred_at AT TIME ZONE 'UTC', 'YYYY-MM-DD')`, `to_char(e.occurred_at AT TIME ZONE 'UTC', 'YYYY-MM-DD')`, nil
	}
	return "", "", fmt.Errorf("%w: unsupported group_by %q", app.ErrInvalid, g)
}

func (s *Store) CostBuckets(ctx context.Context, q app.CostQuery) ([]app.CostBucket, error) {
	key, label, err := groupExprs(q.GroupBy)
	if err != nil {
		return nil, err
	}
	var w where
	if q.UserID != nil {
		w.add("e.user_id = ?", *q.UserID)
	}
	if q.Repo != nil {
		w.add("e.repo = ?", repoKey(*q.Repo))
	}
	if q.Issue != nil {
		w.add("e.repo = ? AND e.issue_number = ?", repoKey(q.Issue.Repo), q.Issue.Number)
	}
	if q.Bucket != "" {
		w.add("e.bucket = ?", string(q.Bucket))
	}
	if q.From != nil {
		w.add("e.occurred_at >= ?", *q.From)
	}
	if q.To != nil {
		w.add("e.occurred_at < ?", *q.To)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+key+` AS k, min(`+label+`), (e.occurred_at AT TIME ZONE 'UTC')::date AS d,
		       sum(e.cost_usd_micros), sum(e.input_tokens), sum(e.output_tokens), sum(e.reasoning_tokens),
		       sum(e.cache_read_tokens), sum(e.cache_write_tokens), count(*), count(*) FILTER (WHERE NOT e.priced)
		FROM usage_events e
		JOIN users u ON u.id = e.user_id
		LEFT JOIN issues i ON i.repo = e.repo AND i.number = e.issue_number`+w.sql()+`
		GROUP BY k, d`, w.args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (app.CostBucket, error) {
		var (
			b    app.CostBucket
			cost int64
		)
		t := &b.Tokens
		err := r.Scan(&b.Key, &b.Label, &b.Day, &cost, &t.Input, &t.Output, &t.Reasoning, &t.CacheRead, &t.CacheWrite, &b.Events, &b.Unpriced)
		b.Cost = domain.Micros(cost)
		return b, err
	})
}
