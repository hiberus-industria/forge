// Package modelsdev seeds the price book from https://models.dev, the open
// model catalog also used by OpenCode.
package modelsdev

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/hiberus-industria/forge/internal/app"
	"github.com/hiberus-industria/forge/internal/domain"
)

// DefaultURL is the models.dev catalog endpoint.
const DefaultURL = "https://models.dev/api.json"

// Source fetches seed prices from models.dev.
type Source struct {
	url string
	hc  *http.Client
	now func() time.Time
}

var _ app.PriceSource = (*Source)(nil)

// New returns a price source for the public catalog.
func New() app.PriceSource { return NewWithURL(DefaultURL, nil) }

// NewWithURL returns a price source for a custom endpoint (tests, mirrors).
func NewWithURL(url string, hc *http.Client) *Source {
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	return &Source{url: url, hc: hc, now: time.Now}
}

// catalog mirrors the subset of api.json forge needs:
// provider id → {models: model id → {cost: USD per 1M tokens}}.
// Context-size tiers ("tiers", "context_over_200k") are ignored: forge prices
// at the base rate.
type catalog map[string]struct {
	Models map[string]struct {
		ID   string `json:"id"`
		Cost *struct {
			Input      *float64 `json:"input"`
			Output     *float64 `json:"output"`
			CacheRead  *float64 `json:"cache_read"`
			CacheWrite *float64 `json:"cache_write"`
		} `json:"cost"`
	} `json:"models"`
}

// FetchPrices returns one price per provider/model with a published cost,
// effective from today (UTC midnight), sorted by provider and model.
func (s *Source) FetchPrices(ctx context.Context) ([]domain.Price, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("models.dev: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models.dev: unexpected status %s", resp.Status)
	}
	var cat catalog
	if err := json.NewDecoder(resp.Body).Decode(&cat); err != nil {
		return nil, fmt.Errorf("models.dev: decode: %w", err)
	}

	y, m, d := s.now().UTC().Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	val := func(p *float64) float64 {
		if p == nil || *p < 0 {
			return 0
		}
		return *p
	}
	var out []domain.Price
	for provider, p := range cat {
		for key, model := range p.Models {
			c := model.Cost
			if c == nil || (c.Input == nil && c.Output == nil) {
				continue
			}
			id := model.ID
			if id == "" {
				id = key
			}
			out = append(out, domain.Price{
				Provider:      provider,
				Model:         id,
				Input:         val(c.Input),
				Output:        val(c.Output),
				CacheRead:     val(c.CacheRead),
				CacheWrite:    val(c.CacheWrite),
				EffectiveFrom: today,
				Source:        domain.PriceSourceModelsDev,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Model < out[j].Model
	})
	return out, nil
}
