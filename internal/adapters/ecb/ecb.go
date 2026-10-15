// Package ecb fetches euro foreign exchange reference rates published daily
// by the European Central Bank (around 16:00 CET on TARGET business days).
package ecb

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hiberus-industria/forge/internal/app"
	"github.com/hiberus-industria/forge/internal/domain"
)

// DefaultBaseURL hosts eurofxref-daily.xml, eurofxref-hist-90d.xml and eurofxref-hist.xml.
const DefaultBaseURL = "https://www.ecb.europa.eu/stats/eurofxref/"

// Source fetches USD per EUR rates.
type Source struct {
	base string
	hc   *http.Client
	now  func() time.Time
}

var _ app.FXSource = (*Source)(nil)

// New returns a source backed by the ECB website.
func New() app.FXSource { return NewWithURL(DefaultBaseURL, nil) }

// NewWithURL uses a custom base URL (tests, mirrors) that serves the same file names.
func NewWithURL(baseURL string, hc *http.Client) *Source {
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}
	return &Source{base: baseURL, hc: hc, now: time.Now}
}

// file picks the smallest feed covering since: the daily file holds only the
// last business day, the 90-day file about three months, the history file
// everything since 1999.
func (s *Source) file(since time.Time) string {
	age := s.now().Sub(since)
	switch {
	case since.IsZero() || age > 85*24*time.Hour:
		return "eurofxref-hist.xml"
	case age > 24*time.Hour:
		return "eurofxref-hist-90d.xml"
	default:
		return "eurofxref-daily.xml"
	}
}

// envelope matches the gesmes envelope: Cube > Cube[time] > Cube[currency,rate].
type envelope struct {
	Cube struct {
		Days []struct {
			Time  string `xml:"time,attr"`
			Rates []struct {
				Currency string `xml:"currency,attr"`
				Rate     string `xml:"rate,attr"`
			} `xml:"Cube"`
		} `xml:"Cube"`
	} `xml:"Cube"`
}

// FetchRates returns USD rates from since (inclusive, by day) in ascending date order.
func (s *Source) FetchRates(ctx context.Context, since time.Time) ([]domain.FXRate, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+s.file(since), nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ecb: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ecb: unexpected status %s", resp.Status)
	}
	var env envelope
	if err := xml.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("ecb: decode: %w", err)
	}
	y, m, d := since.UTC().Date()
	from := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	var out []domain.FXRate
	for _, day := range env.Cube.Days {
		date, err := time.Parse(time.DateOnly, day.Time)
		if err != nil || date.Before(from) {
			continue
		}
		for _, r := range day.Rates {
			if r.Currency != "USD" {
				continue
			}
			v, err := strconv.ParseFloat(r.Rate, 64)
			if err != nil || v <= 0 {
				return nil, fmt.Errorf("ecb: invalid USD rate %q on %s", r.Rate, day.Time)
			}
			out = append(out, domain.FXRate{Date: date, USDPerEUR: v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out, nil
}
