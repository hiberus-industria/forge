package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hiberus-industria/forge/internal/app"
)

const userCols = `id, subject, email, name, coalesce(github_login, '')`

func scanUser(row pgx.Row) (app.User, error) {
	var u app.User
	err := row.Scan(&u.ID, &u.Subject, &u.Email, &u.Name, &u.GitHubLogin)
	return u, mapErr(err)
}

func (s *Store) UpsertUser(ctx context.Context, subject, email, name string) (app.User, error) {
	return scanUser(s.pool.QueryRow(ctx, `
		INSERT INTO users (subject, email, name) VALUES ($1, $2, $3)
		ON CONFLICT (subject) DO UPDATE
		   SET email = CASE WHEN excluded.email <> '' THEN excluded.email ELSE users.email END,
		       name  = CASE WHEN excluded.name  <> '' THEN excluded.name  ELSE users.name  END,
		       updated_at = CASE WHEN (users.email, users.name) IS DISTINCT FROM (excluded.email, excluded.name)
		                         THEN now() ELSE users.updated_at END
		RETURNING `+userCols, subject, email, name))
}

func (s *Store) ListUsers(ctx context.Context) ([]app.User, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+userCols+` FROM users ORDER BY name, email`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (app.User, error) { return scanUser(r) })
}

func (s *Store) UserByLogin(ctx context.Context, login string) (app.User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE lower(github_login) = lower($1)`, login))
}

func (s *Store) SetGitHubLogin(ctx context.Context, id uuid.UUID, login string) (app.User, error) {
	return scanUser(s.pool.QueryRow(ctx, `
		UPDATE users SET github_login = $2, updated_at = now() WHERE id = $1
		RETURNING `+userCols, id, login))
}
