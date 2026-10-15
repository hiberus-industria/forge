// Package opencode integrates OpenCode with forge through a global plugin
// (https://opencode.ai/docs/plugins/) that posts the usage metadata of every
// completed assistant message (`message.updated` event) to the daemon.
package opencode

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/hiberus-industria/forge/internal/adapters/daemonprobe"
	"github.com/hiberus-industria/forge/internal/domain"
	"github.com/hiberus-industria/forge/internal/harness"
)

//go:embed plugin/forge.js
var pluginSource string

const urlPlaceholder = "__FORGE_INGEST_URL__"

// Payload is what the plugin posts to /ingest/opencode.
type Payload struct {
	Kind       string  `json:"kind"` // "message" | "session"
	ID         string  `json:"id"`
	SessionID  string  `json:"sessionID"`
	ProviderID string  `json:"providerID"`
	ModelID    string  `json:"modelID"`
	Cost       float64 `json:"cost"` // USD
	Tokens     struct {
		Input     int64 `json:"input"`
		Output    int64 `json:"output"`
		Reasoning int64 `json:"reasoning"`
		Cache     struct {
			Read  int64 `json:"read"`
			Write int64 `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
	Time struct {
		Created   int64 `json:"created"`   // unix ms
		Completed int64 `json:"completed"` // unix ms
	} `json:"time"`
	Cwd string `json:"cwd"`
}

// Translate converts a plugin payload. Exactly one of the results is set.
func Translate(p Payload, now time.Time) (*harness.Captured, *harness.SessionStarted, error) {
	switch p.Kind {
	case "session":
		if p.SessionID == "" || p.Cwd == "" {
			return nil, nil, errors.New("session payload without id or cwd")
		}
		return nil, &harness.SessionStarted{Harness: domain.HarnessOpenCode, SessionID: p.SessionID, Cwd: p.Cwd, At: now}, nil
	case "message", "":
		if p.ID == "" || p.ModelID == "" {
			return nil, nil, errors.New("message payload without id or model")
		}
		at := p.Time.Completed
		if at == 0 {
			at = p.Time.Created
		}
		occurred := now
		if at > 0 {
			occurred = time.UnixMilli(at).UTC()
		}
		return &harness.Captured{
			Harness: domain.HarnessOpenCode, ExternalID: p.ID, SessionID: p.SessionID,
			Provider: p.ProviderID, Model: p.ModelID, OccurredAt: occurred, Cwd: p.Cwd,
			ReportedCost: domain.MicrosFromUSD(p.Cost),
			Tokens: domain.Tokens{
				Input: p.Tokens.Input, Output: p.Tokens.Output, Reasoning: p.Tokens.Reasoning,
				CacheRead: p.Tokens.Cache.Read, CacheWrite: p.Tokens.Cache.Write,
			},
		}, nil, nil
	}
	return nil, nil, errors.New("unknown payload kind " + p.Kind)
}

// Handler serves POST /ingest/opencode.
func Handler(sink harness.Sink, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p Payload
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&p); err != nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		c, s, err := Translate(p, time.Now().UTC())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if s != nil {
			err = sink.SessionStarted(r.Context(), *s)
		} else {
			err = sink.Capture(r.Context(), *c)
		}
		if err != nil {
			log.Error("opencode: capture failed", "err", err)
			http.Error(w, "capture failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// ConfigDir is OpenCode's global configuration directory.
func ConfigDir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "opencode")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "opencode")
}

// Integration installs the global plugin.
type Integration struct{ dir string }

// New uses OpenCode's global configuration directory.
func New() *Integration { return &Integration{dir: ConfigDir()} }

// NewAt uses a custom configuration directory (tests).
func NewAt(dir string) *Integration { return &Integration{dir: dir} }

func (i *Integration) Harness() domain.Harness { return domain.HarnessOpenCode }

func (i *Integration) pluginPath() string { return filepath.Join(i.dir, "plugins", "forge.js") }

func (i *Integration) Installed() bool {
	if _, err := exec.LookPath("opencode"); err == nil {
		return true
	}
	_, err := os.Stat(i.dir)
	return err == nil
}

// PluginSource renders the plugin for a daemon endpoint.
func PluginSource(ep harness.Endpoint) string {
	return strings.Replace(pluginSource, urlPlaceholder, ep.BaseURL, 1)
}

func (i *Integration) current() (string, error) {
	b, err := os.ReadFile(i.pluginPath())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

func (i *Integration) PlanSetup(ep harness.Endpoint) (harness.Plan, error) {
	plan := harness.Plan{Harness: domain.HarnessOpenCode}
	cur, err := i.current()
	if err != nil {
		return plan, err
	}
	want := PluginSource(ep)
	if cur == want {
		return plan, nil
	}
	plan.Changes = []harness.FileChange{{Path: i.pluginPath(), Before: cur, After: want}}
	plan.Notes = []string{"restart running OpenCode sessions to load the forge plugin"}
	return plan, nil
}

func (i *Integration) PlanRemove() (harness.Plan, error) {
	plan := harness.Plan{Harness: domain.HarnessOpenCode}
	cur, err := i.current()
	if err != nil || cur == "" {
		return plan, err
	}
	if !strings.Contains(cur, "forge-managed") {
		return plan, errors.New(i.pluginPath() + " was not created by forge; remove it manually")
	}
	plan.Changes = []harness.FileChange{{Path: i.pluginPath(), Before: cur, After: ""}}
	return plan, nil
}

func (i *Integration) Doctor(ctx context.Context, ep harness.Endpoint) []harness.CheckResult {
	cur, err := i.current()
	r := harness.CheckResult{Name: "OpenCode plugin", OK: err == nil && cur == PluginSource(ep), Detail: i.pluginPath()}
	if !r.OK {
		r.Fix = "run `forge setup`"
	}
	return append([]harness.CheckResult{r},
		daemonprobe.Checks(ctx, ep, domain.HarnessOpenCode, "restart OpenCode after `forge setup` and send a prompt")...)
}

var _ harness.Integration = (*Integration)(nil)
