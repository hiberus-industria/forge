package daemon

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/hiberus-industria/forge/internal/adapters/claudecode"
	"github.com/hiberus-industria/forge/internal/adapters/daemonprobe"
	"github.com/hiberus-industria/forge/internal/adapters/opencode"
	"github.com/hiberus-industria/forge/internal/domain"
)

// listenIngest binds the harness telemetry listener, loopback only.
func listenIngest(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("ingest_addr %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("ingest_addr %q must be a loopback address", addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s (is another forge daemon running, or the port taken? set ingest_addr): %w", addr, err)
	}
	return ln, nil
}

func (d *Daemon) ingestHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /v1/logs", claudecode.LogsHandler(d.svc, d.log))
	mux.Handle("POST /v1/metrics", claudecode.IgnoreHandler())
	mux.Handle("POST /v1/traces", claudecode.IgnoreHandler())
	mux.Handle("POST /hooks/claude-code", claudecode.HookHandler(d.svc, d.log))
	mux.Handle("POST /ingest/opencode", opencode.Handler(d.svc, d.log))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		h := daemonprobe.Health{Status: "ok", LastCapture: map[domain.Harness]time.Time{}}
		stats, err := d.store.Stats(r.Context(), time.Now())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		for _, s := range stats {
			h.LastCapture[s.Harness] = s.LastEventAt
		}
		writeJSON(w, http.StatusOK, h)
	})
	return loopbackOnly(mux)
}

// loopbackOnly rejects browser-originated requests (a web page could
// otherwise POST to localhost) and DNS-rebinding attempts.
func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" { // browsers always send it on cross-origin POSTs
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
