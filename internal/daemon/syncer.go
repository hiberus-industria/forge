package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/hiberus-industria/forge/internal/adapters/sqlite"
	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/auth"
	"github.com/hiberus-industria/forge/internal/daemon/localapi"
	"github.com/hiberus-industria/forge/internal/domain"
)

const (
	batchSize    = 500
	syncInterval = 15 * time.Second
	maxBackoff   = 5 * time.Minute
)

// syncer pushes buffered events to the API, idempotently.
type syncer struct {
	store  *sqlite.Store
	client *api.ClientWithResponses
	log    *slog.Logger

	trigger chan struct{}
	flushMu sync.Mutex // one flush at a time

	mu       sync.Mutex
	lastSync *time.Time
	lastErr  string
}

func newSyncer(store *sqlite.Store, client *api.ClientWithResponses, log *slog.Logger) *syncer {
	return &syncer{store: store, client: client, log: log, trigger: make(chan struct{}, 1)}
}

// nudge asks for a sync soon without blocking.
func (s *syncer) nudge() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

func (s *syncer) state() (*time.Time, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSync, s.lastErr
}

func (s *syncer) record(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.lastErr = err.Error()
		return
	}
	now := time.Now().UTC()
	s.lastSync, s.lastErr = &now, ""
}

// run syncs on a timer and on nudges, backing off exponentially on failure.
// Nudges are debounced by a second so bursts of requests share a batch.
func (s *syncer) run(ctx context.Context) error {
	backoff := time.Duration(0)
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	next := time.Now().Add(time.Second)
	schedule := func(d time.Duration) {
		timer.Reset(d)
		next = time.Now().Add(d)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.trigger:
			// During backoff wait; otherwise sync within a second.
			if backoff == 0 && time.Until(next) > time.Second {
				schedule(time.Second)
			}
			continue
		case <-timer.C:
		}
		_, err := s.flush(ctx)
		switch {
		case err == nil:
			backoff = 0
			schedule(syncInterval)
		case errors.Is(err, auth.ErrNotLoggedIn):
			backoff = 0 // nothing to retry until login; reload nudges us
			schedule(syncInterval)
		default:
			backoff = min(max(2*backoff, syncInterval), maxBackoff)
			s.log.Warn("sync failed", "err", err, "retry_in", backoff)
			schedule(backoff)
		}
	}
}

// flush sends every pending event.
func (s *syncer) flush(ctx context.Context) (localapi.SyncResult, error) {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	var res localapi.SyncResult
	for {
		pending, err := s.store.Pending(ctx, batchSize)
		if err != nil {
			return res, err
		}
		if len(pending) == 0 {
			break
		}
		r, err := s.send(ctx, pending)
		res.Sent += r.Sent
		res.Duplicates += r.Duplicates
		res.Rejected += r.Rejected
		if err != nil {
			s.record(err)
			res.Pending, _ = s.store.CountPending(ctx)
			return res, err
		}
		if len(pending) < batchSize {
			break
		}
	}
	s.record(nil)
	res.Pending, _ = s.store.CountPending(ctx)
	return res, nil
}

func (s *syncer) send(ctx context.Context, events []domain.UsageEvent) (localapi.SyncResult, error) {
	var res localapi.SyncResult
	body := api.IngestRequest{Events: make([]api.UsageEventInput, len(events))}
	for i, e := range events {
		body.Events[i] = toAPI(e)
	}
	resp, err := s.client.IngestUsageEventsWithResponse(ctx, body)
	if err != nil {
		return res, err
	}
	if resp.JSON200 == nil {
		msg := resp.Status()
		if resp.JSONDefault != nil {
			msg = fmt.Sprintf("%s: %s", resp.Status(), resp.JSONDefault.Message)
		}
		if resp.StatusCode() == http.StatusUnauthorized {
			return res, fmt.Errorf("%w (%s)", auth.ErrNotLoggedIn, msg)
		}
		return res, fmt.Errorf("ingest: %s", msg)
	}
	rejected := map[string]string{}
	for _, r := range resp.JSON200.Rejected {
		rejected[r.ExternalId] = r.Reason
	}
	accepted := make([]string, 0, len(events))
	for _, e := range events {
		if reason, ok := rejected[e.ExternalID]; ok {
			s.log.Warn("event rejected by the API", "harness", e.Harness, "id", e.ExternalID, "reason", reason)
			if err := s.store.MarkRejected(ctx, e.DedupKey(), reason); err != nil {
				return res, err
			}
			continue
		}
		accepted = append(accepted, e.DedupKey())
	}
	if err := s.store.MarkSynced(ctx, accepted, time.Now()); err != nil {
		return res, err
	}
	res.Sent = resp.JSON200.Accepted
	res.Duplicates = resp.JSON200.Duplicates
	res.Rejected = len(resp.JSON200.Rejected)
	return res, nil
}

func toAPI(e domain.UsageEvent) api.UsageEventInput {
	in := api.UsageEventInput{
		ExternalId: e.ExternalID, Harness: api.Harness(e.Harness), SessionId: e.SessionID,
		Model: e.Model, OccurredAt: e.OccurredAt, MachineId: e.MachineID,
		Tokens: api.Tokens{
			Input: e.Tokens.Input, Output: e.Tokens.Output, Reasoning: e.Tokens.Reasoning,
			CacheRead: e.Tokens.CacheRead, CacheWrite: e.Tokens.CacheWrite,
		},
		Attribution: api.Attribution{Bucket: api.Bucket(e.Attribution.Bucket), Source: api.AttributionSource(e.Attribution.Source)},
	}
	if e.Provider != "" {
		in.Provider = &e.Provider
	}
	if e.ReportedCost != 0 {
		rc := int64(e.ReportedCost)
		in.ReportedCostUsdMicros = &rc
	}
	a := e.Attribution
	if a.Repo != nil {
		r := a.Repo.String()
		in.Attribution.Repo = &r
	}
	if a.Branch != "" {
		b := a.Branch
		in.Attribution.Branch = &b
	}
	if a.Issue != nil {
		in.Attribution.Issue = &api.IssueRef{Repo: a.Issue.Repo.String(), Number: a.Issue.Number}
	}
	return in
}
