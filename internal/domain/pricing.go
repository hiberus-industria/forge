package domain

import (
	"math/big"
	"sort"
	"strings"
	"time"
)

// PriceSource tells where a price came from. Manual prices always win over
// seeded ones for the same model and effective date.
type PriceSource string

const (
	PriceSourceModelsDev PriceSource = "models.dev"
	PriceSourceManual    PriceSource = "manual"
)

// Price is the USD price of a model per million tokens, valid from
// EffectiveFrom until superseded by a later price for the same key.
type Price struct {
	ID int64
	// Provider is optional; an empty provider matches any provider.
	Provider      string
	Model         string
	Input         float64 // USD per 1M input tokens
	Output        float64 // USD per 1M output tokens (reasoning is billed as output)
	CacheRead     float64 // USD per 1M cache-read tokens
	CacheWrite    float64 // USD per 1M cache-write tokens
	EffectiveFrom time.Time
	Source        PriceSource
}

// Cost computes the cost of the given tokens with exact rational arithmetic,
// rounding to the nearest micro-dollar.
func (p Price) Cost(t Tokens) Micros {
	sum := new(big.Rat)
	add := func(n int64, perM float64) {
		if n == 0 || perM == 0 {
			return
		}
		r := new(big.Rat).SetFloat64(perM)
		if r == nil {
			return
		}
		sum.Add(sum, r.Mul(r, new(big.Rat).SetInt64(n)))
	}
	add(t.Input, p.Input)
	add(t.Output+t.Reasoning, p.Output)
	add(t.CacheRead, p.CacheRead)
	add(t.CacheWrite, p.CacheWrite)
	// tokens * USD/1M tokens * 1e6 micros/USD == tokens * USD-per-M  (in micros)
	f, _ := sum.Float64()
	return Micros(int64(f + 0.5))
}

// NormalizeModel canonicalizes model identifiers coming from different
// harnesses and providers so that they can be matched against the price book:
// lowercased, without provider prefixes ("anthropic/", "openrouter/x/") and
// without Bedrock/Vertex style suffixes ("@20250101", "-v1:0").
func NormalizeModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndexByte(m, '/'); i >= 0 {
		m = m[i+1:]
	}
	if i := strings.IndexByte(m, '@'); i >= 0 {
		m = m[:i]
	}
	m = strings.TrimSuffix(m, "-v1:0")
	m = strings.TrimSuffix(m, "[1m]")
	return m
}

// PriceBook resolves the price applicable to a request.
type PriceBook struct {
	byModel map[string][]Price // sorted by EffectiveFrom desc, manual first on ties
}

// NewPriceBook indexes a set of prices.
func NewPriceBook(prices []Price) *PriceBook {
	pb := &PriceBook{byModel: make(map[string][]Price)}
	for _, p := range prices {
		k := NormalizeModel(p.Model)
		pb.byModel[k] = append(pb.byModel[k], p)
	}
	for _, ps := range pb.byModel {
		sort.SliceStable(ps, func(i, j int) bool {
			if !ps[i].EffectiveFrom.Equal(ps[j].EffectiveFrom) {
				return ps[i].EffectiveFrom.After(ps[j].EffectiveFrom)
			}
			return ps[i].Source == PriceSourceManual && ps[j].Source != PriceSourceManual
		})
	}
	return pb
}

// Lookup returns the price for provider/model at the given instant. A price
// with a matching provider is preferred over a provider-agnostic one.
func (pb *PriceBook) Lookup(provider, model string, at time.Time) (Price, bool) {
	cands := pb.byModel[NormalizeModel(model)]
	provider = strings.ToLower(provider)
	var fallback *Price
	for i := range cands {
		p := cands[i]
		if p.EffectiveFrom.After(at) {
			continue
		}
		pp := strings.ToLower(p.Provider)
		if pp == provider && provider != "" {
			return p, true
		}
		if fallback == nil && (pp == "" || provider == "" || p.Source == PriceSourceManual) {
			fallback = &cands[i]
		}
	}
	if fallback != nil {
		return *fallback, true
	}
	// Last resort: any provider's price for the model.
	for _, p := range cands {
		if !p.EffectiveFrom.After(at) {
			return p, true
		}
	}
	return Price{}, false
}

// Price prices an event. When the model is unknown to the price book the
// harness-reported cost is used and priced is false so callers can flag it.
func (pb *PriceBook) Price(e UsageEvent) (cost Micros, priceID int64, priced bool) {
	if p, ok := pb.Lookup(e.Provider, e.Model, e.OccurredAt); ok {
		return p.Cost(e.Tokens), p.ID, true
	}
	return e.ReportedCost, 0, false
}
