package modelsdev

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hiberus-industria/forge/internal/domain"
)

// fixture follows the real api.json shape, including fields forge ignores.
const fixture = `{
  "anthropic": {"id":"anthropic","name":"Anthropic","models":{
    "claude-haiku-4-5":{"id":"claude-haiku-4-5","name":"Claude Haiku 4.5","limit":{"context":200000},
      "cost":{"input":1,"output":5,"cache_read":0.1,"cache_write":1.25}},
    "claude-opus":{"id":"claude-opus","cost":{"input":5,"output":25,"cache_read":0.5,"cache_write":6.25,
      "tiers":[{"input":10,"output":37.5,"tier":{"type":"context","size":200000}}],"context_over_200k":{"input":10,"output":37.5}}}
  }},
  "zhipu": {"id":"zhipu","models":{
    "glm-5":{"id":"glm-5","cost":{"input":0.6,"output":2.2,"reasoning":2.2}},
    "free-model":{"id":"free-model"},
    "embedding":{"id":"embedding","cost":{"input_audio":1}}
  }}
}`

func TestFetchPrices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fixture))
	}))
	defer srv.Close()
	s := NewWithURL(srv.URL, srv.Client())
	s.now = func() time.Time { return time.Date(2026, 10, 15, 17, 30, 0, 0, time.FixedZone("CEST", 2*3600)) }

	prices, err := s.FetchPrices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(prices) != 3 {
		t.Fatalf("got %d prices: %+v", len(prices), prices)
	}
	want := domain.Price{
		Provider: "anthropic", Model: "claude-haiku-4-5", Input: 1, Output: 5, CacheRead: 0.1, CacheWrite: 1.25,
		EffectiveFrom: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), Source: domain.PriceSourceModelsDev,
	}
	if prices[0] != want {
		t.Fatalf("got %+v\nwant %+v", prices[0], want)
	}
	if p := prices[1]; p.Model != "claude-opus" || p.Input != 5 || p.Output != 25 {
		t.Fatalf("tiers must not override base price: %+v", p)
	}
	if p := prices[2]; p.Provider != "zhipu" || p.Model != "glm-5" || p.CacheRead != 0 {
		t.Fatalf("unexpected %+v", p)
	}
}

func TestFetchPricesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	if _, err := NewWithURL(srv.URL, srv.Client()).FetchPrices(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}
