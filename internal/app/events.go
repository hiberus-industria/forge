package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hiberus-industria/forge/internal/domain"
)

// EventQuery is the public filter of ListEvents.
type EventQuery struct {
	User      string // "me", GitHub login or empty
	Repo      string // owner/name
	Issue     int
	Bucket    domain.Bucket
	SessionID string
	From, To  *time.Time
	Cursor    string
	Limit     int
}

// EventPage is a page of events, newest first.
type EventPage struct {
	Items      []StoredEvent
	NextCursor string
}

// ListEvents lists usage events with keyset pagination.
func (s *Service) ListEvents(ctx context.Context, q EventQuery) (EventPage, error) {
	me, err := s.CurrentUser(ctx)
	if err != nil {
		return EventPage{}, err
	}
	f := EventFilter{Issue: q.Issue, Bucket: q.Bucket, SessionID: q.SessionID, From: q.From, To: q.To, Limit: q.Limit}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	if f.UserID, err = s.resolveUser(ctx, me, q.User); err != nil {
		return EventPage{}, err
	}
	if f.Repo, err = parseRepoFilter(q.Repo); err != nil {
		return EventPage{}, err
	}
	if f.Issue > 0 && f.Repo == nil {
		return EventPage{}, invalid("issue filter requires repo")
	}
	if q.Cursor != "" {
		c, err := decodeCursor(q.Cursor)
		if err != nil {
			return EventPage{}, err
		}
		f.After = &c
	}
	items, err := s.store.ListEvents(ctx, f)
	if err != nil {
		return EventPage{}, err
	}
	page := EventPage{Items: items}
	if len(items) == f.Limit {
		last := items[len(items)-1]
		page.NextCursor = encodeCursor(EventCursor{OccurredAt: last.OccurredAt, ID: last.ID})
	}
	return page, nil
}

// ReassignResult summarizes a reassignment.
type ReassignResult struct {
	Updated int
	Skipped int
}

// Reassign moves events to an issue (or back to the inbox when to is nil).
// Members may move their own events, admins any event.
func (s *Service) Reassign(ctx context.Context, ids []uuid.UUID, to *domain.IssueRef, reason string) (ReassignResult, error) {
	me, err := s.CurrentUser(ctx)
	if err != nil {
		return ReassignResult{}, err
	}
	if len(ids) == 0 {
		return ReassignResult{}, invalid("event_ids is required")
	}
	if len(ids) > 5000 {
		return ReassignResult{}, invalid("at most 5000 events per reassignment")
	}
	if to != nil {
		t := canonIssue(*to)
		if t.Number <= 0 {
			return ReassignResult{}, invalid("invalid issue number")
		}
		if !s.inOrg(t.Repo) {
			return ReassignResult{}, invalid("issue %s is outside organization %s", t, s.org)
		}
		to = &t
	}
	evs, err := s.store.EventsByIDs(ctx, ids)
	if err != nil {
		return ReassignResult{}, err
	}
	res := ReassignResult{Skipped: len(ids) - len(evs)}
	var changes []Reassignment
	for _, e := range evs {
		if e.User.ID != me.ID && !me.Admin {
			res.Skipped++
			continue
		}
		next := e.Attribution.Reassign(to)
		if attributionEqual(e.Attribution, next) {
			continue
		}
		changes = append(changes, Reassignment{EventID: e.ID, From: e.Attribution, To: next})
	}
	if len(changes) == 0 {
		return res, nil
	}
	if to != nil {
		if err := s.store.EnsureIssues(ctx, []domain.IssueRef{*to}); err != nil {
			return ReassignResult{}, err
		}
	}
	if err := s.store.Reassign(ctx, me.ID, strings.TrimSpace(reason), changes); err != nil {
		return ReassignResult{}, err
	}
	res.Updated = len(changes)
	return res, nil
}

func attributionEqual(a, b domain.Attribution) bool {
	if a.Bucket != b.Bucket || a.Source != b.Source || a.Branch != b.Branch {
		return false
	}
	if (a.Issue == nil) != (b.Issue == nil) || (a.Repo == nil) != (b.Repo == nil) {
		return false
	}
	if a.Issue != nil && *a.Issue != *b.Issue {
		return false
	}
	return a.Repo == nil || *a.Repo == *b.Repo
}

// InboxQuery filters the inbox.
type InboxQuery struct {
	User     string
	Repo     string
	From, To *time.Time
}

// Inbox lists unassigned spend grouped by session for bulk triage.
func (s *Service) Inbox(ctx context.Context, q InboxQuery) ([]InboxGroup, error) {
	me, err := s.CurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	var f InboxFilter
	f.From, f.To = q.From, q.To
	if f.UserID, err = s.resolveUser(ctx, me, q.User); err != nil {
		return nil, err
	}
	if f.Repo, err = parseRepoFilter(q.Repo); err != nil {
		return nil, err
	}
	return s.store.Inbox(ctx, f)
}

func parseRepoFilter(s string) (*domain.RepoRef, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	r, err := domain.ParseRepoRef(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	r = canonRepo(r)
	return &r, nil
}

func encodeCursor(c EventCursor) string {
	raw := fmt.Sprintf("%d|%s", c.OccurredAt.UnixNano(), c.ID)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(s string) (EventCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return EventCursor{}, invalid("bad cursor")
	}
	ts, id, ok := strings.Cut(string(b), "|")
	if !ok {
		return EventCursor{}, invalid("bad cursor")
	}
	var nanos int64
	if _, err := fmt.Sscan(ts, &nanos); err != nil {
		return EventCursor{}, invalid("bad cursor")
	}
	uid, err := uuid.Parse(id)
	if err != nil {
		return EventCursor{}, invalid("bad cursor")
	}
	return EventCursor{OccurredAt: time.Unix(0, nanos).UTC(), ID: uid}, nil
}
