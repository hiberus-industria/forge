package postgres

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hiberus-industria/forge/internal/app"
	"github.com/hiberus-industria/forge/internal/domain"
)

// attrCols flattens an attribution into its columns.
func attrCols(a domain.Attribution) (repo *string, issue *int) {
	if a.Repo != nil {
		k := repoKey(*a.Repo)
		repo = &k
	}
	if a.Issue != nil {
		k := repoKey(a.Issue.Repo)
		repo = &k
		n := a.Issue.Number
		issue = &n
	}
	return repo, issue
}

func (s *Store) InsertEvents(ctx context.Context, evs []app.NewEvent) (int, error) {
	b := &pgx.Batch{}
	for _, e := range evs {
		repo, issue := attrCols(e.Attribution)
		var priceID *int64
		if e.PriceID != 0 {
			priceID = &e.PriceID
		}
		t := e.Tokens
		b.Queue(`
			INSERT INTO usage_events (dedup_key, user_id, harness, external_id, session_id, provider, model, machine_id,
				input_tokens, output_tokens, reasoning_tokens, cache_read_tokens, cache_write_tokens,
				occurred_at, bucket, source, repo, branch, issue_number,
				reported_cost_usd_micros, cost_usd_micros, price_id, priced)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)
			ON CONFLICT (dedup_key) DO NOTHING`,
			e.DedupKey(), e.UserID, string(e.Harness), e.ExternalID, e.SessionID, e.Provider, e.Model, e.MachineID,
			t.Input, t.Output, t.Reasoning, t.CacheRead, t.CacheWrite,
			e.OccurredAt, string(e.Attribution.Bucket), string(e.Attribution.Source), repo, e.Attribution.Branch, issue,
			int64(e.ReportedCost), int64(e.Cost), priceID, e.Priced)
	}
	inserted := 0
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		br := tx.SendBatch(ctx, b)
		defer br.Close()
		for range evs {
			tag, err := br.Exec()
			if err != nil {
				return err
			}
			inserted += int(tag.RowsAffected())
		}
		return br.Close()
	})
	return inserted, mapErr(err)
}

const eventCols = `e.id, e.harness, e.external_id, e.session_id, e.provider, e.model, e.machine_id,
	e.input_tokens, e.output_tokens, e.reasoning_tokens, e.cache_read_tokens, e.cache_write_tokens,
	e.occurred_at, e.received_at, e.bucket, e.source, coalesce(e.repo, ''), e.branch, e.issue_number,
	e.reported_cost_usd_micros, e.cost_usd_micros, coalesce(e.price_id, 0), e.priced,
	u.id, u.name, coalesce(u.github_login, '')`

func scanEvent(row pgx.Row) (app.StoredEvent, error) {
	var (
		e                    app.StoredEvent
		harness, bucket, src string
		repo                 string
		issue                *int
		reported, cost       int64
	)
	t := &e.Tokens
	err := row.Scan(&e.ID, &harness, &e.ExternalID, &e.SessionID, &e.Provider, &e.Model, &e.MachineID,
		&t.Input, &t.Output, &t.Reasoning, &t.CacheRead, &t.CacheWrite,
		&e.OccurredAt, &e.ReceivedAt, &bucket, &src, &repo, &e.Attribution.Branch, &issue,
		&reported, &cost, &e.PriceID, &e.Priced,
		&e.User.ID, &e.User.Name, &e.User.GitHubLogin)
	if err != nil {
		return e, mapErr(err)
	}
	e.Harness = domain.Harness(harness)
	e.ReportedCost, e.Cost = domain.Micros(reported), domain.Micros(cost)
	e.Attribution.Bucket = domain.Bucket(bucket)
	e.Attribution.Source = domain.AttributionSource(src)
	e.Attribution.Repo = parseRepoKey(repo)
	if issue != nil && e.Attribution.Repo != nil {
		e.Attribution.Issue = &domain.IssueRef{Repo: *e.Attribution.Repo, Number: *issue}
	}
	return e, nil
}

func (s *Store) ListEvents(ctx context.Context, f app.EventFilter) ([]app.StoredEvent, error) {
	var w where
	if f.UserID != nil {
		w.add("e.user_id = ?", *f.UserID)
	}
	if f.Repo != nil {
		w.add("e.repo = ?", repoKey(*f.Repo))
	}
	if f.Issue > 0 {
		w.add("e.issue_number = ?", f.Issue)
	}
	if f.Bucket != "" {
		w.add("e.bucket = ?", string(f.Bucket))
	}
	if f.SessionID != "" {
		w.add("e.session_id = ?", f.SessionID)
	}
	if f.Model != "" {
		w.add("lower(e.model) = lower(?)", f.Model)
	}
	if f.From != nil {
		w.add("e.occurred_at >= ?", *f.From)
	}
	if f.To != nil {
		w.add("e.occurred_at < ?", *f.To)
	}
	if f.After != nil {
		w.add("(e.occurred_at, e.id) < (?, ?)", f.After.OccurredAt, f.After.ID)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT ` + eventCols + ` FROM usage_events e JOIN users u ON u.id = e.user_id` + w.sql() +
		` ORDER BY e.occurred_at DESC, e.id DESC LIMIT ` + w.next(limit)
	rows, err := s.pool.Query(ctx, q, w.args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (app.StoredEvent, error) { return scanEvent(r) })
}

func (s *Store) EventsByIDs(ctx context.Context, ids []uuid.UUID) ([]app.StoredEvent, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+eventCols+` FROM usage_events e JOIN users u ON u.id = e.user_id
		WHERE e.id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (app.StoredEvent, error) { return scanEvent(r) })
}

func (s *Store) Reassign(ctx context.Context, actor uuid.UUID, reason string, changes []app.Reassignment) error {
	return mapErr(s.inTx(ctx, func(tx pgx.Tx) error {
		b := &pgx.Batch{}
		for _, c := range changes {
			repo, issue := attrCols(c.To)
			from, _ := json.Marshal(c.From)
			to, _ := json.Marshal(c.To)
			b.Queue(`UPDATE usage_events SET bucket = $2, source = $3, repo = $4, branch = $5, issue_number = $6 WHERE id = $1`,
				c.EventID, string(c.To.Bucket), string(c.To.Source), repo, c.To.Branch, issue)
			b.Queue(`INSERT INTO reassignments (event_id, actor_id, from_attribution, to_attribution, reason) VALUES ($1,$2,$3,$4,$5)`,
				c.EventID, actor, from, to, reason)
		}
		return tx.SendBatch(ctx, b).Close()
	}))
}

func (s *Store) UpdateCosts(ctx context.Context, ups []app.CostUpdate) error {
	b := &pgx.Batch{}
	for _, u := range ups {
		var pid *int64
		if u.PriceID != 0 {
			pid = &u.PriceID
		}
		b.Queue(`UPDATE usage_events SET cost_usd_micros = $2, price_id = $3, priced = $4 WHERE id = $1`,
			u.EventID, int64(u.Cost), pid, u.Priced)
	}
	return mapErr(s.inTx(ctx, func(tx pgx.Tx) error { return tx.SendBatch(ctx, b).Close() }))
}

func (s *Store) Inbox(ctx context.Context, f app.InboxFilter) ([]app.InboxGroup, error) {
	var w where
	w.add("e.bucket <> 'issue'")
	if f.UserID != nil {
		w.add("e.user_id = ?", *f.UserID)
	}
	if f.Repo != nil {
		w.add("e.repo = ?", repoKey(*f.Repo))
	}
	if f.From != nil {
		w.add("e.occurred_at >= ?", *f.From)
	}
	if f.To != nil {
		w.add("e.occurred_at < ?", *f.To)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT e.bucket, coalesce(e.repo, ''), e.branch, e.harness, e.session_id,
		       u.id, u.name, coalesce(u.github_login, ''),
		       min(e.occurred_at), max(e.occurred_at), count(*),
		       array_agg(DISTINCT e.model), sum(e.cost_usd_micros), array_agg(e.id ORDER BY e.occurred_at)
		FROM usage_events e JOIN users u ON u.id = e.user_id`+w.sql()+`
		GROUP BY e.bucket, e.repo, e.branch, e.harness, e.session_id, u.id
		ORDER BY max(e.occurred_at) DESC
		LIMIT 500`, w.args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (app.InboxGroup, error) {
		var (
			g               app.InboxGroup
			bucket, harness string
			repo            string
			cost            int64
		)
		err := r.Scan(&bucket, &repo, &g.Branch, &harness, &g.SessionID,
			&g.User.ID, &g.User.Name, &g.User.GitHubLogin,
			&g.FirstAt, &g.LastAt, &g.Events, &g.Models, &cost, &g.EventIDs)
		g.Bucket, g.Harness, g.Cost = domain.Bucket(bucket), domain.Harness(harness), domain.Micros(cost)
		g.Repo = parseRepoKey(repo)
		return g, err
	})
}
