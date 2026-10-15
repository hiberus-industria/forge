package postgres

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hiberus-industria/forge/internal/domain"
)

const priceCols = `id, provider, model, input, output, cache_read, cache_write, effective_from, source`

func scanPrice(row pgx.Row) (domain.Price, error) {
	var (
		p   domain.Price
		src string
	)
	err := row.Scan(&p.ID, &p.Provider, &p.Model, &p.Input, &p.Output, &p.CacheRead, &p.CacheWrite, &p.EffectiveFrom, &src)
	p.Source = domain.PriceSource(src)
	return p, mapErr(err)
}

func (s *Store) ListPrices(ctx context.Context, model string) ([]domain.Price, error) {
	var w where
	if model != "" {
		w.add("model ILIKE ?", "%"+escapeLike(model)+"%")
	}
	rows, err := s.pool.Query(ctx, `SELECT `+priceCols+` FROM prices`+w.sql()+
		` ORDER BY lower(model), provider, effective_from DESC, id DESC`, w.args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Price, error) { return scanPrice(r) })
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (s *Store) InsertPrice(ctx context.Context, p domain.Price, createdBy *uuid.UUID) (domain.Price, error) {
	return scanPrice(s.pool.QueryRow(ctx, `
		INSERT INTO prices (provider, model, input, output, cache_read, cache_write, effective_from, source, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING `+priceCols,
		p.Provider, p.Model, p.Input, p.Output, p.CacheRead, p.CacheWrite, p.EffectiveFrom, string(p.Source), createdBy))
}

func (s *Store) UpsertSeedPrices(ctx context.Context, ps []domain.Price) (int, error) {
	n := 0
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		for _, p := range ps {
			var cur domain.Price
			err := tx.QueryRow(ctx, `
				SELECT input, output, cache_read, cache_write FROM prices
				WHERE source = 'models.dev' AND lower(provider) = lower($1) AND lower(model) = lower($2)
				ORDER BY effective_from DESC, id DESC LIMIT 1`, p.Provider, p.Model).
				Scan(&cur.Input, &cur.Output, &cur.CacheRead, &cur.CacheWrite)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err == nil && samePrice(cur, p) {
				continue
			}
			eff := p.EffectiveFrom
			if eff.IsZero() {
				eff = time.Now()
			}
			if errors.Is(err, pgx.ErrNoRows) {
				// First time this model is seen: make the seed apply to past usage too.
				eff = time.Unix(0, 0).UTC()
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO prices (provider, model, input, output, cache_read, cache_write, effective_from, source)
				VALUES ($1,$2,$3,$4,$5,$6,$7,'models.dev')`,
				p.Provider, p.Model, p.Input, p.Output, p.CacheRead, p.CacheWrite, eff); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, mapErr(err)
}

func samePrice(a, b domain.Price) bool {
	eq := func(x, y float64) bool { return math.Abs(x-y) < 1e-9 }
	return eq(a.Input, b.Input) && eq(a.Output, b.Output) && eq(a.CacheRead, b.CacheRead) && eq(a.CacheWrite, b.CacheWrite)
}

func (s *Store) UpsertFXRates(ctx context.Context, rates []domain.FXRate) error {
	if len(rates) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, r := range rates {
		b.Queue(`INSERT INTO fx_rates (day, usd_per_eur) VALUES ($1, $2)
			ON CONFLICT (day) DO UPDATE SET usd_per_eur = excluded.usd_per_eur`, r.Date.UTC().Truncate(24*time.Hour), r.USDPerEUR)
	}
	return mapErr(s.inTx(ctx, func(tx pgx.Tx) error { return tx.SendBatch(ctx, b).Close() }))
}

func (s *Store) FXRates(ctx context.Context, since time.Time) ([]domain.FXRate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT day, usd_per_eur FROM fx_rates
		WHERE day >= coalesce((SELECT max(day) FROM fx_rates WHERE day <= $1::date), $1::date)
		ORDER BY day`, since.UTC())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.FXRate, error) {
		var fx domain.FXRate
		err := r.Scan(&fx.Date, &fx.USDPerEUR)
		return fx, err
	})
}

func (s *Store) LatestFXDay(ctx context.Context) (time.Time, error) {
	var d *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT max(day) FROM fx_rates`).Scan(&d); err != nil {
		return time.Time{}, err
	}
	if d == nil {
		return time.Time{}, nil
	}
	return *d, nil
}

func (s *Store) GetMeta(ctx context.Context, key string) (string, error) {
	var v string
	err := s.pool.QueryRow(ctx, `SELECT value FROM meta WHERE key = $1`, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO meta (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = now()`, key, value)
	return err
}
