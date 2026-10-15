package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/hiberus-industria/forge/internal/domain"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func event(id string, at time.Time) domain.UsageEvent {
	repo := domain.RepoRef{Owner: "o", Name: "r"}
	return domain.UsageEvent{
		ExternalID: id, Harness: domain.HarnessClaudeCode, SessionID: "s", Model: "m", MachineID: "mid",
		Tokens: domain.Tokens{Input: 10, Output: 5}, OccurredAt: at, ReportedCost: 7,
		Attribution: domain.Attribution{
			Bucket: domain.BucketIssue, Source: domain.SourceBranch, Repo: &repo, Branch: "feat/1-x",
			Issue: &domain.IssueRef{Repo: repo, Number: 1},
		},
	}
}

func TestEventsLifecycle(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	now := time.Now().UTC().Truncate(time.Millisecond)

	ok, err := s.InsertEvent(ctx, event("a", now), 100)
	if err != nil || !ok {
		t.Fatalf("insert: %v %v", ok, err)
	}
	if ok, _ := s.InsertEvent(ctx, event("a", now), 100); ok {
		t.Fatal("duplicate inserted")
	}
	s.InsertEvent(ctx, event("b", now.Add(time.Second)), 1)
	s.InsertEvent(ctx, event("c", now.Add(2*time.Second)), 1)

	p, err := s.Pending(ctx, 10)
	if err != nil || len(p) != 3 || p[0].ExternalID != "a" {
		t.Fatalf("pending: %v %v", p, err)
	}
	if p[0].Attribution.Issue.Number != 1 || p[0].Tokens.Output != 5 || !p[0].OccurredAt.Equal(now) {
		t.Fatalf("round trip: %+v", p[0])
	}
	if err := s.MarkSynced(ctx, []string{p[0].DedupKey(), p[1].DedupKey()}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRejected(ctx, p[2].DedupKey(), "bad"); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountPending(ctx); n != 0 {
		t.Fatalf("pending = %d", n)
	}
	rec, _ := s.Recent(ctx, 10)
	if len(rec) != 2 || rec[0].Event.ExternalID != "b" || rec[0].Status != StatusSynced {
		t.Fatalf("recent: %+v", rec)
	}
	st, _ := s.Stats(ctx, now.Add(-time.Hour))
	if len(st) != 1 || st[0].EventsSince != 3 {
		t.Fatalf("stats: %+v", st)
	}
	if n, _ := s.Prune(ctx, now.Add(time.Hour)); n != 3 {
		t.Fatalf("pruned %d", n)
	}
}

func TestSessionsKVAndMachineID(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.UpsertSession(ctx, domain.HarnessOpenCode, "x", "/a", time.Now()); err != nil {
		t.Fatal(err)
	}
	s.UpsertSession(ctx, domain.HarnessOpenCode, "x", "/b", time.Now())
	if cwd, _ := s.SessionCwd(ctx, domain.HarnessOpenCode, "x"); cwd != "/b" {
		t.Fatalf("cwd = %q", cwd)
	}
	if cwd, _ := s.SessionCwd(ctx, domain.HarnessClaudeCode, "x"); cwd != "" {
		t.Fatal("session leaked across harnesses")
	}
	id1, _ := s.MachineID(ctx)
	id2, _ := s.MachineID(ctx)
	if id1 == "" || id1 != id2 {
		t.Fatalf("machine id not stable: %q %q", id1, id2)
	}
	var v struct{ A int }
	if found, _ := s.GetJSON(ctx, "k", &v); found {
		t.Fatal("unexpected value")
	}
	s.SetJSON(ctx, "k", struct{ A int }{3})
	if found, _ := s.GetJSON(ctx, "k", &v); !found || v.A != 3 {
		t.Fatal("json kv round trip")
	}
}
