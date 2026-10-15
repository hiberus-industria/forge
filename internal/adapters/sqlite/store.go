// Package sqlite is the daemon's local buffer: captured events waiting to be
// synced, harness sessions (session → working directory) and small state.
// It never leaves the developer machine.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" database/sql driver

	"github.com/hiberus-industria/forge/internal/domain"
)

//go:embed migrations/*.sql
var migrations embed.FS

// EventStatus is the sync state of a buffered event.
type EventStatus string

const (
	StatusPending  EventStatus = "pending"
	StatusSynced   EventStatus = "synced"
	StatusRejected EventStatus = "rejected"
)

// StoredEvent is a buffered event with its local state.
type StoredEvent struct {
	Event    domain.UsageEvent
	Estimate domain.Micros
	Status   EventStatus
}

// Store is the SQLite-backed buffer.
type Store struct{ db *sql.DB }

// Open opens (creating if needed) the database and applies migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY)`); err != nil {
		return err
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE name = ?`, name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		b, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(b)); err != nil {
			tx.Rollback()
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(name) VALUES (?)`, name); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------- key/value

// ErrNoValue is returned by GetKV for a missing key.
var ErrNoValue = errors.New("no value")

func (s *Store) GetKV(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM kv WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoValue
	}
	return v, err
}

func (s *Store) SetKV(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO kv(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *Store) DeleteKV(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM kv WHERE key = ?`, key)
	return err
}

// GetJSON decodes a JSON value; found is false when missing.
func (s *Store) GetJSON(ctx context.Context, key string, v any) (found bool, err error) {
	raw, err := s.GetKV(ctx, key)
	if errors.Is(err, ErrNoValue) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), v)
}

// SetJSON stores a JSON value.
func (s *Store) SetJSON(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.SetKV(ctx, key, string(b))
}

// MachineID returns this machine's stable id, generating it on first use.
func (s *Store) MachineID(ctx context.Context) (string, error) {
	id, err := s.GetKV(ctx, "machine_id")
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, ErrNoValue) {
		return "", err
	}
	id = uuid.NewString()
	return id, s.SetKV(ctx, "machine_id", id)
}

// ---------------------------------------------------------------- sessions

// UpsertSession records the working directory of a harness session.
func (s *Store) UpsertSession(ctx context.Context, harness domain.Harness, sessionID, cwd string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions(harness, session_id, cwd, started_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(harness, session_id) DO UPDATE SET cwd = excluded.cwd`,
		string(harness), sessionID, cwd, at.UnixMilli())
	return err
}

// SessionCwd returns the working directory of a session, "" when unknown.
func (s *Store) SessionCwd(ctx context.Context, harness domain.Harness, sessionID string) (string, error) {
	var cwd string
	err := s.db.QueryRowContext(ctx, `SELECT cwd FROM sessions WHERE harness = ? AND session_id = ?`, string(harness), sessionID).Scan(&cwd)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return cwd, err
}

// ---------------------------------------------------------------- events

// InsertEvent buffers an event; inserted is false for a duplicate.
func (s *Store) InsertEvent(ctx context.Context, e domain.UsageEvent, estimate domain.Micros) (inserted bool, err error) {
	attr, err := json.Marshal(e.Attribution)
	if err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO events(dedup_key, harness, external_id, session_id, provider, model,
			input, output, reasoning, cache_read, cache_write,
			occurred_at, reported_cost, estimate, machine_id, attribution, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?)
		ON CONFLICT(dedup_key) DO NOTHING`,
		e.DedupKey(), string(e.Harness), e.ExternalID, e.SessionID, e.Provider, e.Model,
		e.Tokens.Input, e.Tokens.Output, e.Tokens.Reasoning, e.Tokens.CacheRead, e.Tokens.CacheWrite,
		e.OccurredAt.UnixMilli(), int64(e.ReportedCost), int64(estimate), e.MachineID, string(attr), time.Now().UnixMilli())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

const eventCols = `harness, external_id, session_id, provider, model, input, output, reasoning, cache_read, cache_write,
	occurred_at, reported_cost, estimate, machine_id, attribution, status`

func scanEvent(sc interface{ Scan(...any) error }) (StoredEvent, error) {
	var (
		se                StoredEvent
		e                 = &se.Event
		harness, attr, st string
		occurred          int64
		reported, est     int64
	)
	if err := sc.Scan(&harness, &e.ExternalID, &e.SessionID, &e.Provider, &e.Model,
		&e.Tokens.Input, &e.Tokens.Output, &e.Tokens.Reasoning, &e.Tokens.CacheRead, &e.Tokens.CacheWrite,
		&occurred, &reported, &est, &e.MachineID, &attr, &st); err != nil {
		return se, err
	}
	e.Harness = domain.Harness(harness)
	e.OccurredAt = time.UnixMilli(occurred).UTC()
	e.ReportedCost = domain.Micros(reported)
	se.Estimate = domain.Micros(est)
	se.Status = EventStatus(st)
	return se, json.Unmarshal([]byte(attr), &e.Attribution)
}

func (s *Store) queryEvents(ctx context.Context, q string, args ...any) ([]StoredEvent, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredEvent
	for rows.Next() {
		se, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, se)
	}
	return out, rows.Err()
}

// Pending returns the oldest events awaiting sync.
func (s *Store) Pending(ctx context.Context, limit int) ([]domain.UsageEvent, error) {
	ses, err := s.queryEvents(ctx, `SELECT `+eventCols+` FROM events WHERE status = 'pending' ORDER BY occurred_at LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	out := make([]domain.UsageEvent, len(ses))
	for i, se := range ses {
		out[i] = se.Event
	}
	return out, nil
}

// Recent returns the newest events regardless of status.
func (s *Store) Recent(ctx context.Context, limit int) ([]StoredEvent, error) {
	return s.queryEvents(ctx, `SELECT `+eventCols+` FROM events WHERE status != 'rejected' ORDER BY occurred_at DESC LIMIT ?`, limit)
}

// CountPending returns the number of events awaiting sync.
func (s *Store) CountPending(ctx context.Context) (int, error) {
	var n int
	return n, s.db.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE status = 'pending'`).Scan(&n)
}

// MarkSynced flags events (by dedup key) as accepted by the API.
func (s *Store) MarkSynced(ctx context.Context, keys []string, at time.Time) error {
	if len(keys) == 0 {
		return nil
	}
	args := make([]any, 0, len(keys)+1)
	args = append(args, at.UnixMilli())
	for _, k := range keys {
		args = append(args, k)
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE events SET status = 'synced', synced_at = ? WHERE status = 'pending' AND dedup_key IN (?`+strings.Repeat(",?", len(keys)-1)+`)`,
		args...)
	return err
}

// MarkRejected flags an event as permanently rejected by the API.
func (s *Store) MarkRejected(ctx context.Context, key, reason string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE events SET status = 'rejected', reject_reason = ? WHERE dedup_key = ?`, reason, key)
	return err
}

// Prune deletes synced events older than before.
func (s *Store) Prune(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE status IN ('synced', 'rejected') AND occurred_at < ?`, before.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// HarnessStats summarizes captures of one harness.
type HarnessStats struct {
	Harness     domain.Harness
	LastEventAt time.Time
	EventsSince int
}

// Stats returns, per harness, the last capture time and the number of
// events since the given instant.
func (s *Store) Stats(ctx context.Context, since time.Time) ([]HarnessStats, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT harness, max(occurred_at), sum(CASE WHEN occurred_at >= ? THEN 1 ELSE 0 END)
		FROM events GROUP BY harness ORDER BY harness`, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HarnessStats
	for rows.Next() {
		var h string
		var last int64
		var n int
		if err := rows.Scan(&h, &last, &n); err != nil {
			return nil, err
		}
		out = append(out, HarnessStats{Harness: domain.Harness(h), LastEventAt: time.UnixMilli(last).UTC(), EventsSince: n})
	}
	return out, rows.Err()
}
