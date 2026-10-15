package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hiberus-industria/forge/internal/app"
	"github.com/hiberus-industria/forge/internal/domain"
)

const repoCols = `full_name, node_id, default_branch, archived, project, status_map, synced_at`

func scanRepo(row pgx.Row) (app.Repo, error) {
	var (
		r             app.Repo
		name          string
		project, smap []byte
		synced        *time.Time
	)
	if err := row.Scan(&name, &r.NodeID, &r.DefaultBranch, &r.Archived, &project, &smap, &synced); err != nil {
		return r, mapErr(err)
	}
	if ref := parseRepoKey(name); ref != nil {
		r.Ref = *ref
	}
	r.SyncedAt = synced
	if len(project) > 0 {
		r.Project = &app.ProjectInfo{}
		if err := json.Unmarshal(project, r.Project); err != nil {
			return r, err
		}
	}
	if len(smap) > 0 {
		r.StatusMap = &domain.StatusMap{}
		if err := json.Unmarshal(smap, r.StatusMap); err != nil {
			return r, err
		}
	}
	return r, nil
}

func (s *Store) ListRepos(ctx context.Context) ([]app.Repo, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+repoCols+` FROM repos ORDER BY full_name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (app.Repo, error) { return scanRepo(r) })
}

func (s *Store) GetRepo(ctx context.Context, ref domain.RepoRef) (app.Repo, error) {
	return scanRepo(s.pool.QueryRow(ctx, `SELECT `+repoCols+` FROM repos WHERE full_name = $1`, repoKey(ref)))
}

func jsonOrNil(v any, isNil bool) ([]byte, error) {
	if isNil {
		return nil, nil
	}
	return json.Marshal(v)
}

func (s *Store) UpsertRepo(ctx context.Context, r app.Repo) error {
	project, err := jsonOrNil(r.Project, r.Project == nil)
	if err != nil {
		return err
	}
	smap, err := jsonOrNil(r.StatusMap, r.StatusMap == nil)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO repos (full_name, node_id, default_branch, archived, project, status_map, synced_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (full_name) DO UPDATE SET node_id = excluded.node_id, default_branch = excluded.default_branch,
			archived = excluded.archived, project = excluded.project, status_map = excluded.status_map,
			synced_at = excluded.synced_at`,
		repoKey(r.Ref), r.NodeID, r.DefaultBranch, r.Archived, project, smap, r.SyncedAt)
	return mapErr(err)
}

func (s *Store) SaveStatusMap(ctx context.Context, ref domain.RepoRef, sm domain.StatusMap) error {
	b, err := json.Marshal(sm)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE repos SET status_map = $2 WHERE full_name = $1`, repoKey(ref), b)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return app.ErrNotFound
	}
	return nil
}
