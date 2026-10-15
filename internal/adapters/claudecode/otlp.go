// Package claudecode integrates Claude Code with forge.
//
// Capture: Claude Code exports OpenTelemetry log events to the daemon
// (OTLP/HTTP). Each `claude_code.api_request` event is one model request
// with tokens, estimated cost, request_id and session.id
// (https://code.claude.com/docs/en/monitoring-usage). The session working
// directory comes from a SessionStart command hook
// (https://code.claude.com/docs/en/hooks) or, as a fallback, from the
// session transcript under ~/.claude/projects.
package claudecode

import (
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/hiberus-industria/forge/internal/domain"
	"github.com/hiberus-industria/forge/internal/harness"
)

const maxBody = 8 << 20

// An OTLP ExportLogsServiceRequest has the same wire (and JSON) shape as
// logs.v1.LogsData (field 1: resource_logs). Decoding into LogsData avoids
// importing the collector package and, with it, gRPC.

// ParseLogs extracts api_request events from an OTLP logs export.
func ParseLogs(req *logspb.LogsData) []harness.Captured {
	var out []harness.Captured
	for _, rl := range req.GetResourceLogs() {
		resAttrs := attrMap(rl.GetResource().GetAttributes())
		for _, sl := range rl.GetScopeLogs() {
			for _, lr := range sl.GetLogRecords() {
				attrs := attrMap(lr.GetAttributes())
				if !isAPIRequest(lr.GetEventName(), lr.GetBody().GetStringValue(), attrs) {
					continue
				}
				get := func(k string) *commonpb.AnyValue {
					if v, ok := attrs[k]; ok {
						return v
					}
					return resAttrs[k]
				}
				c := harness.Captured{
					Harness:    domain.HarnessClaudeCode,
					ExternalID: str(get("request_id")),
					SessionID:  str(get("session.id")),
					Provider:   "anthropic",
					Model:      str(get("model")),
					Tokens: domain.Tokens{
						Input:      num(get("input_tokens")),
						Output:     num(get("output_tokens")),
						CacheRead:  num(get("cache_read_tokens")),
						CacheWrite: num(get("cache_creation_tokens")),
					},
					OccurredAt: eventTime(lr.GetTimeUnixNano(), lr.GetObservedTimeUnixNano(), str(get("event.timestamp"))),
				}
				if micros := get("cost_usd_micros"); micros != nil {
					c.ReportedCost = domain.Micros(num(micros))
				} else {
					c.ReportedCost = domain.MicrosFromUSD(float(get("cost_usd")))
				}
				if c.ExternalID == "" {
					c.ExternalID = str(get("client_request_id"))
				}
				if c.ExternalID == "" || c.Model == "" {
					continue
				}
				out = append(out, c)
			}
		}
	}
	return out
}

func isAPIRequest(eventName, body string, attrs map[string]*commonpb.AnyValue) bool {
	for _, n := range []string{eventName, body, str(attrs["event.name"])} {
		if n == "api_request" || n == "claude_code.api_request" {
			return true
		}
	}
	return false
}

func attrMap(kvs []*commonpb.KeyValue) map[string]*commonpb.AnyValue {
	m := make(map[string]*commonpb.AnyValue, len(kvs))
	for _, kv := range kvs {
		m[kv.GetKey()] = kv.GetValue()
	}
	return m
}

func str(v *commonpb.AnyValue) string {
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_IntValue:
		return strconv.FormatInt(x.IntValue, 10)
	case *commonpb.AnyValue_DoubleValue:
		return strconv.FormatFloat(x.DoubleValue, 'f', -1, 64)
	}
	return ""
}

// num reads integers that exporters may encode as int, double or string.
func num(v *commonpb.AnyValue) int64 {
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_IntValue:
		return x.IntValue
	case *commonpb.AnyValue_DoubleValue:
		return int64(math.Round(x.DoubleValue))
	case *commonpb.AnyValue_StringValue:
		if n, err := strconv.ParseInt(strings.TrimSpace(x.StringValue), 10, 64); err == nil {
			return n
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(x.StringValue), 64); err == nil {
			return int64(math.Round(f))
		}
	}
	return 0
}

func float(v *commonpb.AnyValue) float64 {
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_DoubleValue:
		return x.DoubleValue
	case *commonpb.AnyValue_IntValue:
		return float64(x.IntValue)
	case *commonpb.AnyValue_StringValue:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x.StringValue), 64)
		return f
	}
	return 0
}

func eventTime(nanos, observed uint64, iso string) time.Time {
	if nanos > 0 {
		return time.Unix(0, int64(nanos)).UTC()
	}
	if t, err := time.Parse(time.RFC3339Nano, iso); err == nil {
		return t.UTC()
	}
	if observed > 0 {
		return time.Unix(0, int64(observed)).UTC()
	}
	return time.Now().UTC()
}

// DecodeLogs reads an OTLP/HTTP logs body (protobuf or JSON, optionally gzip).
func DecodeLogs(r *http.Request) (*logspb.LogsData, error) {
	var body io.Reader = http.MaxBytesReader(nil, r.Body, maxBody)
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(body)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		body = io.LimitReader(gz, maxBody)
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	req := &logspb.LogsData{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		err = protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(b, req)
	} else {
		err = proto.Unmarshal(b, req)
	}
	if err != nil {
		return nil, fmt.Errorf("decode otlp logs: %w", err)
	}
	return req, nil
}

// LogsHandler serves POST /v1/logs. It always answers with an empty OTLP
// success so that the exporter never retries forever on our behalf; capture
// failures are logged instead.
func LogsHandler(sink harness.Sink, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, err := DecodeLogs(r)
		if err != nil {
			log.Warn("claude-code: bad otlp payload", "err", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for _, c := range ParseLogs(req) {
			if err := sink.Capture(r.Context(), c); err != nil {
				log.Error("claude-code: capture failed", "request_id", c.ExternalID, "err", err)
			}
		}
		writeOTLPSuccess(w, r)
	})
}

// IgnoreHandler accepts other OTLP signals (metrics, traces) and drops them.
func IgnoreHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, http.MaxBytesReader(nil, r.Body, maxBody))
		writeOTLPSuccess(w, r)
	})
}

func writeOTLPSuccess(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
		return
	}
	// An empty ExportLogsServiceResponse encodes to zero bytes.
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
}
