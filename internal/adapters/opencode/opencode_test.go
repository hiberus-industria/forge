package opencode

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hiberus-industria/forge/internal/domain"
	"github.com/hiberus-industria/forge/internal/harness"
)

type sink struct {
	c []harness.Captured
	s []harness.SessionStarted
}

func (f *sink) Capture(_ context.Context, c harness.Captured) error { f.c = append(f.c, c); return nil }

func (f *sink) SessionStarted(_ context.Context, s harness.SessionStarted) error {
	f.s = append(f.s, s)
	return nil
}

func TestHandler(t *testing.T) {
	f := &sink{}
	h := Handler(f, slog.New(slog.NewTextHandler(io.Discard, nil)))
	msg := `{"kind":"message","id":"msg_1","sessionID":"ses_1","providerID":"litellm","modelID":"glm-5","cost":0.25,
	  "tokens":{"input":10,"output":20,"reasoning":5,"cache":{"read":100,"write":7}},
	  "time":{"created":1759312800000,"completed":1759312805000},"cwd":"/w/repo"}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/ingest/opencode", strings.NewReader(msg)))
	if rec.Code != http.StatusNoContent || len(f.c) != 1 {
		t.Fatalf("code %d %+v", rec.Code, f.c)
	}
	c := f.c[0]
	want := domain.Tokens{Input: 10, Output: 20, Reasoning: 5, CacheRead: 100, CacheWrite: 7}
	if c.ExternalID != "msg_1" || c.Provider != "litellm" || c.Model != "glm-5" || c.Tokens != want ||
		c.ReportedCost != 250000 || c.Cwd != "/w/repo" || !c.OccurredAt.Equal(time.UnixMilli(1759312805000)) {
		t.Fatalf("unexpected %+v", c)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/ingest/opencode", strings.NewReader(`{"kind":"session","sessionID":"ses_1","cwd":"/w"}`)))
	if rec.Code != http.StatusNoContent || len(f.s) != 1 || f.s[0].Cwd != "/w" {
		t.Fatalf("session: %d %+v", rec.Code, f.s)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/ingest/opencode", strings.NewReader(`{"kind":"message"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid payload accepted: %d", rec.Code)
	}
}

func TestIntegrationPlans(t *testing.T) {
	dir := t.TempDir()
	in := NewAt(dir)
	ep := harness.Endpoint{BaseURL: "http://127.0.0.1:47318"}
	p, err := in.PlanSetup(ep)
	if err != nil || len(p.Changes) != 1 || !strings.Contains(p.Changes[0].After, `const FORGE_URL = "http://127.0.0.1:47318"`) {
		t.Fatalf("%v %+v", err, p)
	}
	if err := harness.Apply(p); err != nil {
		t.Fatal(err)
	}
	if p, _ := in.PlanSetup(ep); len(p.Changes) != 0 {
		t.Fatal("setup not idempotent")
	}
	rm, err := in.PlanRemove()
	if err != nil || len(rm.Changes) != 1 || rm.Changes[0].After != "" {
		t.Fatalf("%v %+v", err, rm)
	}
	harness.Apply(rm)
	if _, err := os.Stat(filepath.Join(dir, "plugins", "forge.js")); !os.IsNotExist(err) {
		t.Fatal("plugin not removed")
	}
	os.WriteFile(filepath.Join(dir, "plugins", "forge.js"), []byte("user plugin"), 0o644)
	if _, err := in.PlanRemove(); err == nil {
		t.Fatal("removing a foreign plugin must fail")
	}
}
