// Package daemonprobe checks the daemon's loopback ingest listener from
// harness integrations (`forge doctor`), without going through the socket.
package daemonprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/hiberus-industria/forge/internal/domain"
	"github.com/hiberus-industria/forge/internal/harness"
)

// Health is served by the daemon at GET <ingest>/healthz.
type Health struct {
	Status string `json:"status"`
	// LastCapture is the last captured request per harness.
	LastCapture map[domain.Harness]time.Time `json:"last_capture"`
}

// Probe fetches the daemon health.
func Probe(ctx context.Context, baseURL string) (Health, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/healthz", nil)
	if err != nil {
		return Health{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Health{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Health{}, fmt.Errorf("daemon answered %s", resp.Status)
	}
	var h Health
	return h, json.NewDecoder(resp.Body).Decode(&h)
}

// Checks returns the common doctor lines: daemon reachable and recent captures.
func Checks(ctx context.Context, ep harness.Endpoint, h domain.Harness, restartHint string) []harness.CheckResult {
	health, err := Probe(ctx, ep.BaseURL)
	if err != nil {
		return []harness.CheckResult{{
			Name: "Daemon reachable", OK: false,
			Detail: fmt.Sprintf("%s: %v", ep.BaseURL, err),
			Fix:    "start it with `forge daemon install` (service) or `forge daemon run`",
		}}
	}
	out := []harness.CheckResult{{Name: "Daemon reachable", OK: true, Detail: ep.BaseURL}}
	last, ok := health.LastCapture[h]
	switch {
	case !ok || last.IsZero():
		out = append(out, harness.CheckResult{
			Name: "Requests captured", OK: false,
			Detail: "no request captured yet", Fix: restartHint,
		})
	case time.Since(last) > 7*24*time.Hour:
		out = append(out, harness.CheckResult{
			Name: "Requests captured", OK: false,
			Detail: "last capture " + last.Local().Format("2006-01-02 15:04"), Fix: restartHint,
		})
	default:
		out = append(out, harness.CheckResult{
			Name: "Requests captured", OK: true,
			Detail: "last capture " + last.Local().Format("2006-01-02 15:04"),
		})
	}
	return out
}
