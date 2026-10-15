package app

import (
	"context"
	"fmt"
	"time"

	"github.com/hiberus-industria/forge/internal/domain"
)

// MaxIngestBatch bounds a single ingest request.
const MaxIngestBatch = 500

// Rejection explains why an event was not accepted.
type Rejection struct {
	ExternalID string
	Reason     string
}

// IngestResult summarizes an ingest call.
type IngestResult struct {
	Accepted   int
	Duplicates int
	Rejected   []Rejection
}

// maxClockSkew tolerates daemons whose clock runs a bit ahead.
const maxClockSkew = 10 * time.Minute

// Ingest stores usage events captured by the caller's daemon. It is
// idempotent: events already stored count as duplicates.
func (s *Service) Ingest(ctx context.Context, evs []domain.UsageEvent) (IngestResult, error) {
	me, err := s.CurrentUser(ctx)
	if err != nil {
		return IngestResult{}, err
	}
	if len(evs) > MaxIngestBatch {
		return IngestResult{}, invalid("at most %d events per batch", MaxIngestBatch)
	}
	pb, err := s.book(ctx)
	if err != nil {
		return IngestResult{}, err
	}
	var (
		res    IngestResult
		rows   []NewEvent
		issues []domain.IssueRef
		seen   = map[string]bool{}
	)
	for _, e := range evs {
		e.Attribution = canonAttribution(e.Attribution)
		if err := s.checkEvent(e); err != nil {
			res.Rejected = append(res.Rejected, Rejection{ExternalID: e.ExternalID, Reason: err.Error()})
			continue
		}
		if seen[e.DedupKey()] {
			res.Duplicates++
			continue
		}
		seen[e.DedupKey()] = true
		cost, pid, priced := pb.Price(e)
		rows = append(rows, NewEvent{UsageEvent: e, UserID: me.ID, Cost: cost, PriceID: pid, Priced: priced})
		if e.Attribution.Issue != nil {
			issues = append(issues, *e.Attribution.Issue)
		}
	}
	if len(rows) == 0 {
		return res, nil
	}
	if len(issues) > 0 {
		if err := s.store.EnsureIssues(ctx, issues); err != nil {
			return IngestResult{}, err
		}
	}
	n, err := s.store.InsertEvents(ctx, rows)
	if err != nil {
		return IngestResult{}, err
	}
	res.Accepted = n
	res.Duplicates += len(rows) - n
	return res, nil
}

func (s *Service) checkEvent(e domain.UsageEvent) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if e.OccurredAt.After(s.now().Add(maxClockSkew)) {
		return fmt.Errorf("occurred_at is in the future")
	}
	a := e.Attribution
	if a.Repo != nil && !s.inOrg(*a.Repo) {
		return fmt.Errorf("repository %s is outside organization %s", a.Repo, s.org)
	}
	if a.Issue != nil && !s.inOrg(a.Issue.Repo) {
		return fmt.Errorf("issue %s is outside organization %s", a.Issue, s.org)
	}
	return nil
}
