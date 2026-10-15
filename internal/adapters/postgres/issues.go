package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hiberus-industria/forge/internal/app"
	"github.com/hiberus-industria/forge/internal/domain"
)

func (s *Store) EnsureIssues(ctx context.Context, refs []domain.IssueRef) error {
	if len(refs) == 0 {
		return nil
	}
	repos := make([]string, len(refs))
	nums := make([]int32, len(refs))
	for i, r := range refs {
		repos[i], nums[i] = repoKey(r.Repo), int32(r.Number)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO issues (repo, number)
		SELECT * FROM unnest($1::text[], $2::int[])
		ON CONFLICT DO NOTHING`, repos, nums)
	return mapErr(err)
}

func (s *Store) UpsertIssue(ctx context.Context, i app.IssueInfo) error {
	assignees, labels := i.Assignees, i.Labels
	if assignees == nil {
		assignees = []string{}
	}
	if labels == nil {
		labels = []string{}
	}
	var updated *time.Time
	if !i.UpdatedAt.IsZero() {
		updated = &i.UpdatedAt
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO issues (repo, number, node_id, title, url, gh_state, assignees, labels, placeholder, gh_updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,false,$9)
		ON CONFLICT (repo, number) DO UPDATE SET node_id = excluded.node_id, title = excluded.title,
			url = excluded.url, gh_state = excluded.gh_state, assignees = excluded.assignees,
			labels = excluded.labels, placeholder = false, gh_updated_at = excluded.gh_updated_at,
			updated_at = now()`,
		repoKey(i.Ref.Repo), i.Ref.Number, i.NodeID, i.Title, i.URL, string(i.State), assignees, labels, updated)
	return mapErr(err)
}

const issueCols = `repo, number, node_id, title, url, gh_state, assignees, labels, placeholder, state, branch,
	started_at, pr_opened_at, coalesce(pr_number, 0), closed_at, snapshot_usd_micros, snapshot_tokens, snapshot_at`

func scanTask(row pgx.Row) (app.TaskRecord, error) {
	var (
		t           app.TaskRecord
		repo        string
		ghState, st string
		snapCost    *int64
		snapTokens  []byte
		snapAt      *time.Time
	)
	err := row.Scan(&repo, &t.Ref.Number, &t.NodeID, &t.Title, &t.URL, &ghState, &t.Assignees, &t.Labels,
		&t.Placeholder, &st, &t.Branch, &t.StartedAt, &t.PROpenedAt, &t.PRNumber, &t.ClosedAt,
		&snapCost, &snapTokens, &snapAt)
	if err != nil {
		return t, mapErr(err)
	}
	if r := parseRepoKey(repo); r != nil {
		t.Ref.Repo = *r
	}
	t.GitHubState = app.IssueState(ghState)
	t.State = domain.TaskState(st)
	if snapCost != nil {
		snap := domain.CostSnapshot{Cost: domain.Micros(*snapCost)}
		if snapAt != nil {
			snap.At = *snapAt
		}
		if len(snapTokens) > 0 {
			_ = json.Unmarshal(snapTokens, &snap.Tokens)
		}
		t.Snapshot = &snap
	}
	return t, nil
}

func (s *Store) GetTask(ctx context.Context, ref domain.IssueRef) (app.TaskRecord, error) {
	return scanTask(s.pool.QueryRow(ctx, `SELECT `+issueCols+` FROM issues WHERE repo = $1 AND number = $2`,
		repoKey(ref.Repo), ref.Number))
}

func (s *Store) ListTasks(ctx context.Context, f app.TaskFilter) ([]app.TaskRecord, error) {
	var w where
	if f.Assignee != "" {
		w.add("EXISTS (SELECT 1 FROM unnest(i.assignees) a WHERE lower(a) = lower(?))", f.Assignee)
	}
	if f.SpentBy != nil {
		w.add("EXISTS (SELECT 1 FROM usage_events e WHERE e.repo = i.repo AND e.issue_number = i.number AND e.user_id = ?)", *f.SpentBy)
	}
	if len(f.States) > 0 {
		states := make([]string, len(f.States))
		for i, s := range f.States {
			states[i] = string(s)
		}
		w.add("i.state = ANY(?)", states)
	}
	if f.Repo != nil {
		w.add("i.repo = ?", repoKey(*f.Repo))
	}
	rows, err := s.pool.Query(ctx, `SELECT `+issueCols+` FROM issues i`+w.sql()+` ORDER BY i.updated_at DESC LIMIT 500`, w.args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (app.TaskRecord, error) { return scanTask(r) })
}

func (s *Store) Placeholders(ctx context.Context, limit int) ([]domain.IssueRef, error) {
	rows, err := s.pool.Query(ctx, `SELECT repo, number FROM issues WHERE placeholder ORDER BY updated_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.IssueRef, error) {
		var (
			repo string
			ref  domain.IssueRef
		)
		if err := r.Scan(&repo, &ref.Number); err != nil {
			return ref, err
		}
		if p := parseRepoKey(repo); p != nil {
			ref.Repo = *p
		}
		return ref, nil
	})
}

func (s *Store) SaveLifecycle(ctx context.Context, t app.TaskRecord) error {
	var (
		snapCost   *int64
		snapTokens []byte
		snapAt     *time.Time
		pr         *int
	)
	if t.Snapshot != nil {
		c := int64(t.Snapshot.Cost)
		snapCost, snapAt = &c, &t.Snapshot.At
		snapTokens, _ = json.Marshal(t.Snapshot.Tokens)
	}
	if t.PRNumber > 0 {
		pr = &t.PRNumber
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE issues SET state = $3, branch = $4, started_at = $5, pr_opened_at = $6, pr_number = $7,
			closed_at = $8, snapshot_usd_micros = $9, snapshot_tokens = $10, snapshot_at = $11, updated_at = now()
		WHERE repo = $1 AND number = $2`,
		repoKey(t.Ref.Repo), t.Ref.Number, string(t.State), t.Branch, t.StartedAt, t.PROpenedAt, pr,
		t.ClosedAt, snapCost, snapTokens, snapAt)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return app.ErrNotFound
	}
	return nil
}

func (s *Store) TaskCosts(ctx context.Context, refs []domain.IssueRef) (map[domain.IssueRef]app.TaskCost, error) {
	out := make(map[domain.IssueRef]app.TaskCost, len(refs))
	if len(refs) == 0 {
		return out, nil
	}
	repos := make([]string, len(refs))
	nums := make([]int32, len(refs))
	for i, r := range refs {
		repos[i], nums[i] = repoKey(r.Repo), int32(r.Number)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT e.repo, e.issue_number, sum(e.cost_usd_micros), sum(e.input_tokens), sum(e.output_tokens),
		       sum(e.reasoning_tokens), sum(e.cache_read_tokens), sum(e.cache_write_tokens), count(*), max(e.occurred_at)
		FROM usage_events e
		JOIN unnest($1::text[], $2::int[]) AS k(repo, number) ON k.repo = e.repo AND k.number = e.issue_number
		GROUP BY e.repo, e.issue_number`, repos, nums)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			repo string
			n    int
			c    app.TaskCost
			cost int64
			last time.Time
		)
		t := &c.Tokens
		if err := rows.Scan(&repo, &n, &cost, &t.Input, &t.Output, &t.Reasoning, &t.CacheRead, &t.CacheWrite, &c.Events, &last); err != nil {
			return nil, err
		}
		c.Cost, c.LastActivity = domain.Micros(cost), &last
		if r := parseRepoKey(repo); r != nil {
			out[domain.IssueRef{Repo: *r, Number: n}] = c
		}
	}
	return out, rows.Err()
}
