package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/proto"

	"github.com/hiberus-industria/forge/internal/adapters/daemonprobe"
	"github.com/hiberus-industria/forge/internal/adapters/sqlite"
	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/auth"
	"github.com/hiberus-industria/forge/internal/config"
	"github.com/hiberus-industria/forge/internal/daemon/localapi"
	"github.com/hiberus-industria/forge/internal/domain"
)

const org = "hiberus-industria"

type fakeSession struct{ loggedIn bool }

func (f *fakeSession) Token(context.Context) (auth.Token, error) {
	if !f.loggedIn {
		return auth.Token{}, auth.ErrNotLoggedIn
	}
	return auth.Token{AccessToken: "tok", ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func (f *fakeSession) Reload(context.Context) error { return nil }
func (f *fakeSession) Identity() (auth.Identity, bool) {
	return auth.Identity{Name: "Dev", Email: "dev@x"}, f.loggedIn
}

// fakeAPI records ingested events and rejects external ids starting with "bad".
type fakeAPI struct {
	mu       sync.Mutex
	received []api.UsageEventInput
	seen     map[string]bool
	status   int
}

func (f *fakeAPI) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/usage-events/batch", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing bearer token")
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.status != 0 {
			w.WriteHeader(f.status)
			json.NewEncoder(w).Encode(api.Error{Code: "x", Message: "nope"})
			return
		}
		var req api.IngestRequest
		json.NewDecoder(r.Body).Decode(&req)
		res := api.IngestResult{Rejected: []struct {
			ExternalId string `json:"external_id"`
			Reason     string `json:"reason"`
		}{}}
		for _, e := range req.Events {
			switch {
			case strings.HasPrefix(e.ExternalId, "bad"):
				res.Rejected = append(res.Rejected, struct {
					ExternalId string `json:"external_id"`
					Reason     string `json:"reason"`
				}{e.ExternalId, "invalid"})
			case f.seen[e.ExternalId]:
				res.Duplicates++
			default:
				f.seen[e.ExternalId] = true
				f.received = append(f.received, e)
				res.Accepted++
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(res)
	})
	mux.HandleFunc("GET /v1/prices", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]api.Price{{Id: 1, Model: "claude-opus-5-5", Input: 10, Output: 50, EffectiveFrom: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), Source: "manual"}})
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		login := "devgh"
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(api.User{Email: "dev@x", Name: "Dev", GithubLogin: &login, Roles: []api.UserRoles{"member"}})
	})
	return mux
}

func gitRepo(t *testing.T, branch string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", branch},
		{"remote", "add", "origin", "https://github.com/" + org + "/forge.git"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	os.MkdirAll(filepath.Join(dir, "pkg"), 0o755)
	return dir
}

type harnessEnv struct {
	d      *Daemon
	client *localapi.Client
	ingest string
	api    *fakeAPI
	cancel context.CancelFunc
	done   chan error
}

func start(t *testing.T) *harnessEnv {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	fa := &fakeAPI{seen: map[string]bool{}}
	srv := httptest.NewServer(fa.handler(t))
	t.Cleanup(srv.Close)

	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cfg := config.Defaults()
	cfg.APIURL, cfg.Org = srv.URL, org

	ingestLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.IngestAddr = ingestLn.Addr().String()
	sock := filepath.Join(t.TempDir(), "d.sock")
	localLn, err := listenSocket(sock)
	if err != nil {
		t.Fatal(err)
	}
	d, err := build(ctx, Options{
		Config: cfg, Version: "test", Session: &fakeSession{loggedIn: true},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, store)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- d.serve(runCtx, ingestLn, localLn) }()
	env := &harnessEnv{d: d, client: localapi.NewClient(sock), ingest: "http://" + cfg.IngestAddr, api: fa, cancel: cancel, done: done}
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("daemon did not stop")
		}
	})
	return env
}

func post(t *testing.T, url, ct string, body []byte) int {
	t.Helper()
	resp, err := http.Post(url, ct, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func otlp(session, requestID, model string) []byte {
	s := func(k, v string) *commonpb.KeyValue {
		return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
	}
	b, _ := proto.Marshal(&logspb.LogsData{ResourceLogs: []*logspb.ResourceLogs{{ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{{
		TimeUnixNano: uint64(time.Now().UnixNano()),
		Attributes: []*commonpb.KeyValue{
			s("event.name", "api_request"), s("session.id", session), s("request_id", requestID),
			s("model", model), s("input_tokens", "1000000"), s("output_tokens", "0"), s("cost_usd", "1.5"),
		},
	}}}}}}})
	return b
}

func waitLive(t *testing.T, ch <-chan localapi.LiveEvent) localapi.LiveEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no live event")
	}
	return localapi.LiveEvent{}
}

func TestEndToEnd(t *testing.T) {
	repo := gitRepo(t, "feat/42-cost-tracking")
	env := start(t)
	ctx := context.Background()

	live, err := env.client.Live(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Prices are fetched at startup; wait for them so the estimate is local.
	deadline := time.Now().Add(5 * time.Second)
	for env.d.prices.estimate(domain.UsageEvent{Model: "claude-opus-5-5", Tokens: domain.Tokens{Input: 1}, OccurredAt: time.Now()}) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("prices not loaded")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Claude Code: session hook then an OTLP request.
	hook := `{"session_id":"s1","cwd":"` + filepath.Join(repo, "pkg") + `","hook_event_name":"SessionStart"}`
	if code := post(t, env.ingest+"/hooks/claude-code", "application/json", []byte(hook)); code != http.StatusNoContent {
		t.Fatalf("hook: %d", code)
	}
	if code := post(t, env.ingest+"/v1/logs", "application/x-protobuf", otlp("s1", "req_1", "claude-opus-5-5")); code != 200 {
		t.Fatalf("logs: %d", code)
	}
	ev := waitLive(t, live)
	if ev.Attribution.Bucket != domain.BucketIssue || ev.Attribution.Issue.Number != 42 || ev.Attribution.Source != domain.SourceBranch {
		t.Fatalf("attribution: %+v", ev.Attribution)
	}
	if ev.CostMicros != 10_000_000 { // 1M input tokens at $10/M from the cached price book
		t.Fatalf("estimate = %d", ev.CostMicros)
	}

	// OpenCode outside any repository → external, no repo data.
	msg := `{"kind":"message","id":"msg_1","sessionID":"o1","providerID":"litellm","modelID":"glm-5","cost":0.1,"tokens":{"input":1,"output":2,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":1,"completed":2},"cwd":"` + t.TempDir() + `"}`
	if code := post(t, env.ingest+"/ingest/opencode", "application/json", []byte(msg)); code != http.StatusNoContent {
		t.Fatalf("opencode: %d", code)
	}
	if ev := waitLive(t, live); ev.Attribution.Bucket != domain.BucketExternal || ev.Attribution.Repo != nil || ev.CostMicros != 100000 {
		t.Fatalf("external: %+v", ev)
	}

	// Manual override scoped to the repository.
	o, err := env.client.SetOverride(ctx, localapi.OverrideRequest{
		Issue: domain.IssueRef{Repo: domain.RepoRef{Owner: org, Name: "forge"}, Number: 7},
		Scope: localapi.ScopeRepo, RepoPath: filepath.Join(repo, "pkg"),
	})
	if err != nil || o.RepoPath != repo {
		t.Fatalf("override: %v %+v", err, o)
	}
	post(t, env.ingest+"/v1/logs", "application/x-protobuf", otlp("s1", "req_2", "claude-opus-5-5"))
	if ev := waitLive(t, live); ev.Attribution.Source != domain.SourceManual || ev.Attribution.Issue.Number != 7 {
		t.Fatalf("override not applied: %+v", ev.Attribution)
	}
	if got, _ := env.client.Override(ctx); got == nil || got.Issue.Number != 7 {
		t.Fatalf("get override: %+v", got)
	}
	if err := env.client.ClearOverride(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := env.client.SetOverride(ctx, localapi.OverrideRequest{Scope: localapi.ScopeGlobal}); err == nil {
		t.Fatal("invalid override accepted")
	}

	// A duplicate OTLP delivery is ignored; a bad event gets rejected by the API.
	post(t, env.ingest+"/v1/logs", "application/x-protobuf", otlp("s1", "req_1", "claude-opus-5-5"))
	post(t, env.ingest+"/v1/logs", "application/x-protobuf", otlp("s1", "bad_1", "claude-opus-5-5"))
	waitLive(t, live)

	res, err := env.client.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pending != 0 {
		t.Fatalf("pending after sync: %+v", res)
	}
	env.api.mu.Lock()
	n := len(env.api.received)
	var first api.UsageEventInput
	for _, e := range env.api.received {
		if e.ExternalId == "req_1" {
			first = e
		}
	}
	env.api.mu.Unlock()
	if n != 3 {
		t.Fatalf("api received %d events", n)
	}
	if first.Attribution.Issue == nil || first.Attribution.Issue.Repo != org+"/forge" || first.MachineId == "" {
		t.Fatalf("api payload: %+v", first)
	}

	recent, err := env.client.Recent(ctx, 10)
	if err != nil || len(recent) != 3 || !recent[0].Synced {
		t.Fatalf("recent: %v %+v", err, recent)
	}

	st, err := env.client.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st.LoggedIn || st.User.GitHubLogin != "devgh" || st.Pending != 0 || st.LastSyncAt == nil || len(st.Captures) != 2 || st.MachineID == "" {
		t.Fatalf("status: %+v", st)
	}
	tok, err := env.client.Token(ctx)
	if err != nil || tok.AccessToken != "tok" {
		t.Fatalf("token: %v %+v", err, tok)
	}

	h, err := daemonprobe.Probe(ctx, env.ingest)
	if err != nil || h.LastCapture[domain.HarnessClaudeCode].IsZero() {
		t.Fatalf("healthz: %v %+v", err, h)
	}

	req, _ := http.NewRequest(http.MethodPost, env.ingest+"/ingest/opencode", strings.NewReader(msg))
	req.Header.Set("Origin", "https://evil.example")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("browser request not rejected: %v", err)
	}
}

func TestSyncErrors(t *testing.T) {
	env := start(t)
	ctx := context.Background()
	post(t, env.ingest+"/v1/logs", "application/x-protobuf", otlp("s", "req_x", "m"))
	time.Sleep(50 * time.Millisecond)

	env.api.mu.Lock()
	env.api.status = http.StatusUnauthorized
	env.api.mu.Unlock()
	if _, err := env.client.Sync(ctx); !errors.Is(err, auth.ErrNotLoggedIn) {
		t.Fatalf("want ErrNotLoggedIn, got %v", err)
	}
	env.api.mu.Lock()
	env.api.status = http.StatusInternalServerError
	env.api.mu.Unlock()
	if _, err := env.client.Sync(ctx); err == nil {
		t.Fatal("expected error")
	}
	st, _ := env.client.Status(ctx)
	if st.Pending != 1 || st.LastSyncError == "" {
		t.Fatalf("status after failure: %+v", st)
	}
	env.api.mu.Lock()
	env.api.status = 0
	env.api.mu.Unlock()
	if res, err := env.client.Sync(ctx); err != nil || res.Pending != 0 || res.Sent != 1 {
		t.Fatalf("recovery: %v %+v", err, res)
	}
}

func TestSocketRefusesSecondDaemon(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := listenSocket(sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if st, _ := os.Stat(sock); st.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v", st.Mode())
	}
	if _, err := listenSocket(sock); err == nil {
		t.Fatal("second listener accepted")
	}
	ln.Close()
	os.WriteFile(sock, nil, 0o600) // stale file
	ln2, err := listenSocket(sock)
	if err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	ln2.Close()
}

func TestListenIngestLoopbackOnly(t *testing.T) {
	if _, err := listenIngest("0.0.0.0:0"); err == nil {
		t.Fatal("non-loopback address accepted")
	}
}
