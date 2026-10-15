package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hiberus-industria/forge/internal/domain"
)

// ReportRow is one group of a cost report.
type ReportRow struct {
	Key    string
	Label  string
	Amount float64 // in the report currency
	Cost   domain.Micros
	Tokens domain.Tokens
	Events int
}

// Report is an aggregated cost report.
type Report struct {
	Currency domain.Currency
	GroupBy  GroupBy
	From, To *time.Time
	Total    float64
	TotalUSD domain.Micros
	Unpriced int
	Rows     []ReportRow
}

// ReportQuery is the public filter of CostReport.
type ReportQuery struct {
	GroupBy  GroupBy
	Currency domain.Currency
	User     string
	Repo     string
	Bucket   domain.Bucket
	From, To *time.Time
}

// CostReport aggregates spend. EUR amounts convert each day's spend with
// that day's ECB rate (or the latest previous one).
func (s *Service) CostReport(ctx context.Context, q ReportQuery) (Report, error) {
	me, err := s.CurrentUser(ctx)
	if err != nil {
		return Report{}, err
	}
	cq := CostQuery{GroupBy: q.GroupBy, Bucket: q.Bucket, From: q.From, To: q.To}
	if cq.UserID, err = s.resolveUser(ctx, me, q.User); err != nil {
		return Report{}, err
	}
	if cq.Repo, err = parseRepoFilter(q.Repo); err != nil {
		return Report{}, err
	}
	cur := q.Currency
	if cur == "" {
		cur = domain.CurrencyUSD
	}
	return s.report(ctx, cq, cur)
}

func (s *Service) report(ctx context.Context, q CostQuery, cur domain.Currency) (Report, error) {
	switch q.GroupBy {
	case GroupRepo, GroupIssue, GroupUser, GroupModel, GroupHarness, GroupDay:
	default:
		return Report{}, invalid("unsupported group_by %q", q.GroupBy)
	}
	if cur != domain.CurrencyUSD && cur != domain.CurrencyEUR {
		return Report{}, invalid("unsupported currency %q", cur)
	}
	buckets, err := s.store.CostBuckets(ctx, q)
	if err != nil {
		return Report{}, err
	}
	conv := func(_ time.Time, m domain.Micros) float64 { return m.USD() }
	if cur == domain.CurrencyEUR && len(buckets) > 0 {
		first := buckets[0].Day
		for _, b := range buckets {
			if b.Day.Before(first) {
				first = b.Day
			}
		}
		rates, err := s.store.FXRates(ctx, first)
		if err != nil {
			return Report{}, err
		}
		if len(rates) == 0 {
			return Report{}, fmt.Errorf("%w: no EUR exchange rates loaded yet", ErrConflict)
		}
		conv = func(day time.Time, m domain.Micros) float64 { return rateFor(rates, day).ToEUR(m) }
	}
	rep := Report{Currency: cur, GroupBy: q.GroupBy, From: q.From, To: q.To}
	rows := map[string]*ReportRow{}
	for _, b := range buckets {
		r := rows[b.Key]
		if r == nil {
			r = &ReportRow{Key: b.Key, Label: b.Label}
			rows[b.Key] = r
		}
		amt := conv(b.Day, b.Cost)
		r.Amount += amt
		r.Cost += b.Cost
		r.Tokens = r.Tokens.Add(b.Tokens)
		r.Events += b.Events
		rep.Total += amt
		rep.TotalUSD += b.Cost
		rep.Unpriced += b.Unpriced
	}
	for _, r := range rows {
		rep.Rows = append(rep.Rows, *r)
	}
	sort.Slice(rep.Rows, func(i, j int) bool {
		a, b := rep.Rows[i], rep.Rows[j]
		if q.GroupBy == GroupDay {
			return a.Key < b.Key
		}
		if a.Cost != b.Cost {
			return a.Cost > b.Cost
		}
		return strings.Compare(a.Key, b.Key) < 0
	})
	return rep, nil
}

// rateFor returns the latest rate on or before day; rates are sorted by
// date ascending. Days before the first known rate use the first rate.
func rateFor(rates []domain.FXRate, day time.Time) domain.FXRate {
	i := sort.Search(len(rates), func(i int) bool { return rates[i].Date.After(day) })
	if i == 0 {
		return rates[0]
	}
	return rates[i-1]
}

// RefreshFX loads ECB rates since the last stored day (90 days on first run).
func (s *Service) RefreshFX(ctx context.Context) (int, error) {
	if s.fx == nil {
		return 0, fmt.Errorf("fx source is not configured")
	}
	since, err := s.store.LatestFXDay(ctx)
	if err != nil {
		return 0, err
	}
	if since.IsZero() {
		since = s.now().AddDate(0, 0, -90)
	}
	rates, err := s.fx.FetchRates(ctx, since)
	if err != nil {
		return 0, err
	}
	return len(rates), s.store.UpsertFXRates(ctx, rates)
}
