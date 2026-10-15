package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/hiberus-industria/forge/internal/domain"
)

const priceBookTTL = time.Minute

// book returns the cached price book, reloading it when stale.
func (s *Service) book(ctx context.Context) (*domain.PriceBook, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.priceBook != nil && s.now().Sub(s.bookAt) < priceBookTTL {
		return s.priceBook, nil
	}
	ps, err := s.store.ListPrices(ctx, "")
	if err != nil {
		return nil, err
	}
	s.priceBook = domain.NewPriceBook(ps)
	s.bookAt = s.now()
	return s.priceBook, nil
}

func (s *Service) invalidateBook() {
	s.mu.Lock()
	s.priceBook = nil
	s.mu.Unlock()
}

// ListPrices returns the price book, optionally filtered by model substring.
func (s *Service) ListPrices(ctx context.Context, model string) ([]domain.Price, error) {
	if _, err := s.CurrentUser(ctx); err != nil {
		return nil, err
	}
	return s.store.ListPrices(ctx, strings.TrimSpace(model))
}

// CreatePrice adds a manual price override (admin).
func (s *Service) CreatePrice(ctx context.Context, p domain.Price) (domain.Price, error) {
	me, err := s.requireAdmin(ctx)
	if err != nil {
		return domain.Price{}, err
	}
	p.Model = strings.TrimSpace(p.Model)
	switch {
	case p.Model == "":
		return domain.Price{}, invalid("model is required")
	case p.Input < 0 || p.Output < 0 || p.CacheRead < 0 || p.CacheWrite < 0:
		return domain.Price{}, invalid("prices must be positive")
	case p.EffectiveFrom.IsZero():
		p.EffectiveFrom = s.now()
	}
	p.Source = domain.PriceSourceManual
	created, err := s.store.InsertPrice(ctx, p, &me.ID)
	if err != nil {
		return domain.Price{}, err
	}
	s.invalidateBook()
	return created, nil
}

// SyncPrices refreshes seed prices from the price source. It is called by
// the daily job (no principal) and by admins.
func (s *Service) SyncPrices(ctx context.Context) (int, error) {
	if _, ok := PrincipalFrom(ctx); ok {
		if _, err := s.requireAdmin(ctx); err != nil {
			return 0, err
		}
	}
	if s.prices == nil {
		return 0, errors.New("price source is not configured")
	}
	ps, err := s.prices.FetchPrices(ctx)
	if err != nil {
		return 0, err
	}
	n, err := s.store.UpsertSeedPrices(ctx, ps)
	if err != nil {
		return 0, err
	}
	s.invalidateBook()
	return n, nil
}

// Reprice recomputes event costs with the current price book (admin).
func (s *Service) Reprice(ctx context.Context, from *time.Time, model string) (int, error) {
	if _, err := s.requireAdmin(ctx); err != nil {
		return 0, err
	}
	s.invalidateBook()
	pb, err := s.book(ctx)
	if err != nil {
		return 0, err
	}
	f := EventFilter{From: from, Model: strings.TrimSpace(model), Limit: 1000}
	total := 0
	for {
		evs, err := s.store.ListEvents(ctx, f)
		if err != nil {
			return total, err
		}
		var ups []CostUpdate
		for _, e := range evs {
			cost, pid, priced := pb.Price(e.UsageEvent)
			if cost != e.Cost || pid != e.PriceID || priced != e.Priced {
				ups = append(ups, CostUpdate{EventID: e.ID, Cost: cost, PriceID: pid, Priced: priced})
			}
		}
		if len(ups) > 0 {
			if err := s.store.UpdateCosts(ctx, ups); err != nil {
				return total, err
			}
			total += len(ups)
		}
		if len(evs) < f.Limit {
			return total, nil
		}
		last := evs[len(evs)-1]
		f.After = &EventCursor{OccurredAt: last.OccurredAt, ID: last.ID}
	}
}

func (s *Service) requireAdmin(ctx context.Context) (Me, error) {
	me, err := s.CurrentUser(ctx)
	if err != nil {
		return Me{}, err
	}
	if !me.Admin {
		return Me{}, ErrForbidden
	}
	return me, nil
}
