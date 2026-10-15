package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/domain"
)

// priceCache keeps a copy of the API price book for local cost estimates in
// the live view. The API remains the authority on cost.
type priceCache struct {
	client *api.ClientWithResponses

	mu        sync.RWMutex
	book      *domain.PriceBook
	fetchedAt time.Time
}

func (p *priceCache) refresh(ctx context.Context) error {
	resp, err := p.client.ListPricesWithResponse(ctx, &api.ListPricesParams{})
	if err != nil {
		return err
	}
	if resp.JSON200 == nil {
		return fmt.Errorf("list prices: %s", resp.Status())
	}
	prices := make([]domain.Price, 0, len(*resp.JSON200))
	for _, ap := range *resp.JSON200 {
		p := domain.Price{
			ID: ap.Id, Model: ap.Model, Input: ap.Input, Output: ap.Output,
			CacheRead: ap.CacheRead, CacheWrite: ap.CacheWrite,
			EffectiveFrom: ap.EffectiveFrom, Source: domain.PriceSource(ap.Source),
		}
		if ap.Provider != nil {
			p.Provider = *ap.Provider
		}
		prices = append(prices, p)
	}
	p.mu.Lock()
	p.book = domain.NewPriceBook(prices)
	p.fetchedAt = time.Now()
	p.mu.Unlock()
	return nil
}

func (p *priceCache) estimate(ev domain.UsageEvent) domain.Micros {
	p.mu.RLock()
	book := p.book
	p.mu.RUnlock()
	if book == nil {
		return ev.ReportedCost
	}
	cost, _, _ := book.Price(ev)
	return cost
}
