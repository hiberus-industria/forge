package claudecode

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"

	"github.com/hiberus-industria/forge/internal/domain"
	"github.com/hiberus-industria/forge/internal/harness"
)

type fakeSink struct {
	mu       sync.Mutex
	captured []harness.Captured
	sessions []harness.SessionStarted
}

func (f *fakeSink) Capture(_ context.Context, c harness.Captured) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captured = append(f.captured, c)
	return nil
}

func (f *fakeSink) SessionStarted(_ context.Context, s harness.SessionStarted) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions = append(f.sessions, s)
	return nil
}

func sv(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func iv(k string, v int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}}
}

func fixture() *logspb.LogsData {
	ts := uint64(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC).UnixNano())
	return &logspb.LogsData{ResourceLogs: []*logspb.ResourceLogs{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{sv("service.name", "claude-code"), sv("session.id", "sess-1")}},
		ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{
			{
				TimeUnixNano: ts,
				Body:         &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "claude_code.api_request"}},
				Attributes: []*commonpb.KeyValue{
					sv("event.name", "api_request"), sv("model", "claude-opus-5-5"), sv("request_id", "req_1"),
					sv("input_tokens", "120"), iv("output_tokens", 30), iv("cache_read_tokens", 1000), iv("cache_creation_tokens", 50),
					sv("cost_usd", "0.0123"),
				},
			},
			{Attributes: []*commonpb.KeyValue{sv("event.name", "user_prompt"), sv("prompt_length", "10")}},
		}}},
	}}}
}

func TestParseLogs(t *testing.T) {
	got := ParseLogs(fixture())
	if len(got) != 1 {
		t.Fatalf("got %d events", len(got))
	}
	c := got[0]
	want := domain.Tokens{Input: 120, Output: 30, CacheRead: 1000, CacheWrite: 50}
	if c.ExternalID != "req_1" || c.SessionID != "sess-1" || c.Model != "claude-opus-5-5" || c.Tokens != want ||
		c.ReportedCost != 12300 || c.Harness != domain.HarnessClaudeCode || c.OccurredAt.Hour() != 10 {
		t.Fatalf("unexpected %+v", c)
	}
}

func TestLogsHandlerProtobufAndJSON(t *testing.T) {
	pb, _ := proto.Marshal(fixture())
	jsonBody := `{"resourceLogs":[{"resource":{"attributes":[{"key":"session.id","value":{"stringValue":"s2"}}]},
	 "scopeLogs":[{"logRecords":[{"timeUnixNano":"1759312800000000000","traceId":"5b8efff798038103d269b633813fc60c",
	 "attributes":[{"key":"event.name","value":{"stringValue":"api_request"}},{"key":"model","value":{"stringValue":"m"}},
	 {"key":"request_id","value":{"stringValue":"req_2"}},{"key":"input_tokens","value":{"intValue":"7"}},
	 {"key":"cost_usd_micros","value":{"intValue":"42"}}]}]}]}]}`
	for name, tc := range map[string]struct {
		ct   string
		body []byte
		id   string
	}{
		"protobuf": {"application/x-protobuf", pb, "req_1"},
		"json":     {"application/json", []byte(jsonBody), "req_2"},
	} {
		sink := &fakeSink{}
		req := httptest.NewRequest(http.MethodPost, "/v1/logs", bytes.NewReader(tc.body))
		req.Header.Set("Content-Type", tc.ct)
		rec := httptest.NewRecorder()
		LogsHandler(sink, discard()).ServeHTTP(rec, req)
		if rec.Code != 200 || len(sink.captured) != 1 || sink.captured[0].ExternalID != tc.id {
			t.Fatalf("%s: code=%d captured=%+v body=%s", name, rec.Code, sink.captured, rec.Body)
		}
		if name == "json" && (sink.captured[0].ReportedCost != 42 || sink.captured[0].Tokens.Input != 7 || sink.captured[0].SessionID != "s2") {
			t.Fatalf("json fields: %+v", sink.captured[0])
		}
	}
}

func TestHookHandler(t *testing.T) {
	sink := &fakeSink{}
	body := `{"session_id":"abc","cwd":"/home/dev/repo","hook_event_name":"SessionStart","source":"startup","prompt":"secret"}`
	rec := httptest.NewRecorder()
	HookHandler(sink, discard()).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/hooks/claude-code", strings.NewReader(body)))
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("code %d body %q", rec.Code, rec.Body)
	}
	if len(sink.sessions) != 1 || sink.sessions[0].SessionID != "abc" || sink.sessions[0].Cwd != "/home/dev/repo" {
		t.Fatalf("sessions %+v", sink.sessions)
	}
}

func TestSettingsSetupIdempotentAndRemove(t *testing.T) {
	dir := t.TempDir()
	orig := `{
  "model": "opus",
  "env": {
    "FOO": "bar",
    "CLAUDE_CODE_ENABLE_TELEMETRY": "1"
  },
  "hooks": {
    "SessionStart": [
      {"matcher": "startup", "hooks": [{"type": "command", "command": "echo hi"}]}
    ]
  },
  "permissions": {"allow": ["Bash(ls:*)"]}
}
`
	path := filepath.Join(dir, "settings.json")
	os.WriteFile(path, []byte(orig), 0o600)
	in := NewAt(dir)
	ep := harness.Endpoint{BaseURL: "http://127.0.0.1:47318"}

	plan, err := in.PlanSetup(ep)
	if err != nil || len(plan.Changes) != 1 {
		t.Fatalf("plan: %v %+v", err, plan)
	}
	after := plan.Changes[0].After
	for _, s := range []string{`"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT": "http://127.0.0.1:47318/v1/logs"`, `"FOO": "bar"`, `echo hi`, hookMarker, `"permissions"`} {
		if !strings.Contains(after, s) {
			t.Fatalf("missing %q in\n%s", s, after)
		}
	}
	if strings.Index(after, `"model"`) > strings.Index(after, `"env"`) {
		t.Fatal("key order not preserved")
	}
	if err := harness.Apply(plan); err != nil {
		t.Fatal(err)
	}
	if p2, _ := in.PlanSetup(ep); len(p2.Changes) != 0 {
		t.Fatalf("second plan not empty:\n%s", p2.Changes[0].After)
	}
	// Port change rewrites the hook instead of duplicating it.
	p3, _ := in.PlanSetup(harness.Endpoint{BaseURL: "http://127.0.0.1:5000"})
	if n := strings.Count(p3.Changes[0].After, hookMarker); n != 1 {
		t.Fatalf("%d forge hooks after port change", n)
	}

	rm, err := in.PlanRemove()
	if err != nil || len(rm.Changes) != 1 {
		t.Fatalf("remove plan: %v %+v", err, rm)
	}
	r := rm.Changes[0].After
	if strings.Contains(r, "OTEL_") || strings.Contains(r, hookMarker) || strings.Contains(r, managedEnvKey) {
		t.Fatalf("remove left forge config:\n%s", r)
	}
	// Pre-existing values that already matched are not forge's to remove.
	if !strings.Contains(r, `"CLAUDE_CODE_ENABLE_TELEMETRY": "1"`) || !strings.Contains(r, "echo hi") {
		t.Fatalf("remove dropped user config:\n%s", r)
	}
	harness.Apply(rm)
	if p, _ := in.PlanRemove(); len(p.Changes) != 0 {
		t.Fatal("second remove not empty")
	}
}

func TestSettingsMissingFile(t *testing.T) {
	in := NewAt(filepath.Join(t.TempDir(), "nested"))
	plan, err := in.PlanSetup(harness.Endpoint{BaseURL: "http://127.0.0.1:1"})
	if err != nil || len(plan.Changes) != 1 || plan.Changes[0].Before != "" {
		t.Fatalf("%v %+v", err, plan)
	}
}

func TestTranscriptCwdAndReconcile(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "-home-dev-repo")
	os.MkdirAll(proj, 0o755)
	lines := []string{
		`{"type":"user","sessionId":"s1","cwd":"/home/dev/repo","timestamp":"2026-10-01T10:00:00Z"}`,
		`{"type":"assistant","sessionId":"s1","cwd":"/home/dev/repo","requestId":"req_9","timestamp":"2026-10-01T10:00:01Z","message":{"id":"msg_1","model":"claude-sonnet-5-5","usage":{"input_tokens":3,"output_tokens":4,"cache_read_input_tokens":5,"cache_creation_input_tokens":6}}}`,
		`{"type":"assistant","sessionId":"s1","cwd":"/home/dev/repo","requestId":"req_9","timestamp":"2026-10-01T10:00:01Z","message":{"id":"msg_1","model":"claude-sonnet-5-5","usage":{"input_tokens":3,"output_tokens":4,"cache_read_input_tokens":5,"cache_creation_input_tokens":6}}}`,
	}
	os.WriteFile(filepath.Join(proj, "s1.jsonl"), []byte(strings.Join(lines, "\n")), 0o600)

	if cwd := SessionCwd(dir, "s1"); cwd != "/home/dev/repo" {
		t.Fatalf("cwd %q", cwd)
	}
	if cwd := SessionCwd(dir, "../x"); cwd != "" {
		t.Fatal("path traversal accepted")
	}
	sink := &fakeSink{}
	n, err := Reconcile(context.Background(), dir, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), sink)
	if err != nil || n != 1 || sink.captured[0].ExternalID != "req_9" || sink.captured[0].Tokens.CacheWrite != 6 {
		t.Fatalf("reconcile: %d %v %+v", n, err, sink.captured)
	}
}
