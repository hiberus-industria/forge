package claudecode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hiberus-industria/forge/internal/adapters/daemonprobe"
	"github.com/hiberus-industria/forge/internal/domain"
	"github.com/hiberus-industria/forge/internal/harness"
)

const (
	// managedEnvKey lists, in settings.json env, the variables forge added or
	// changed, so that `forge setup --remove` only undoes its own work.
	managedEnvKey = "FORGE_MANAGED_ENV"
	hookRoute     = "/hooks/claude-code"
	hookMarker    = "# forge-managed"
)

// Integration wires Claude Code to the daemon through ~/.claude/settings.json:
//   - OpenTelemetry logs export (only the logs signal, so metrics/traces
//     setups pointing elsewhere are untouched) to <daemon>/v1/logs;
//   - a SessionStart command hook posting the session cwd to the daemon.
//     SessionStart only supports command hooks, hence curl.
type Integration struct {
	dir string
}

// New uses Claude Code's configuration directory.
func New() *Integration { return &Integration{dir: ConfigDir()} }

// NewAt uses a custom configuration directory (tests).
func NewAt(dir string) *Integration { return &Integration{dir: dir} }

func (i *Integration) Harness() domain.Harness { return domain.HarnessClaudeCode }

func (i *Integration) settingsPath() string { return filepath.Join(i.dir, "settings.json") }

func (i *Integration) Installed() bool {
	if _, err := exec.LookPath("claude"); err == nil {
		return true
	}
	_, err := os.Stat(i.dir)
	return err == nil
}

type kv struct{ k, v string }

func desiredEnv(ep harness.Endpoint) []kv {
	return []kv{
		{"CLAUDE_CODE_ENABLE_TELEMETRY", "1"},
		{"OTEL_LOGS_EXPORTER", "otlp"},
		{"OTEL_EXPORTER_OTLP_LOGS_PROTOCOL", "http/protobuf"},
		{"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", ep.BaseURL + "/v1/logs"},
		{"OTEL_LOGS_EXPORT_INTERVAL", "5000"},
	}
}

func hookCommand(ep harness.Endpoint) string {
	return fmt.Sprintf("curl -fsS -m 2 -X POST -H 'Content-Type: application/json' --data-binary @- '%s%s' >/dev/null 2>&1 || true %s",
		ep.BaseURL, hookRoute, hookMarker)
}

func (i *Integration) load() (before string, o *object, err error) {
	b, err := os.ReadFile(i.settingsPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", nil, err
	}
	o, err = parseObject(b)
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", i.settingsPath(), err)
	}
	return string(b), o, nil
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func managedList(env *object) []string {
	v, _ := env.get(managedEnvKey)
	s := asString(v)
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// PlanSetup computes the settings.json edits; empty when already configured.
func (i *Integration) PlanSetup(ep harness.Endpoint) (harness.Plan, error) {
	plan := harness.Plan{Harness: domain.HarnessClaudeCode}
	before, o, err := i.load()
	if err != nil {
		return plan, err
	}
	env, err := o.childObject("env")
	if err != nil {
		return plan, err
	}
	managed := managedList(env)
	changed := false
	for _, d := range desiredEnv(ep) {
		cur, has := env.get(d.k)
		if has && asString(cur) == d.v {
			continue
		}
		if has && asString(cur) != "" && strings.Contains(d.k, "ENDPOINT") {
			plan.Notes = append(plan.Notes, fmt.Sprintf("%s was %q; forge replaces it (a backup of settings.json is kept)", d.k, asString(cur)))
		}
		env.set(d.k, d.v)
		if !slices.Contains(managed, d.k) {
			managed = append(managed, d.k)
		}
		changed = true
	}
	if changed {
		env.set(managedEnvKey, strings.Join(managed, ","))
	}
	if v, ok := env.get("OTEL_EXPORTER_OTLP_ENDPOINT"); ok {
		plan.Notes = append(plan.Notes, fmt.Sprintf("OTEL_EXPORTER_OTLP_ENDPOINT=%q is kept for other signals; logs now go to forge", asString(v)))
	}
	if os.Getenv("OTEL_LOGS_EXPORTER") != "" || os.Getenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT") != "" {
		plan.Notes = append(plan.Notes, "OTEL_* variables in your shell environment may take precedence; check `forge doctor`")
	}

	hookChanged, err := ensureHook(o, ep)
	if err != nil {
		return plan, err
	}
	if !changed && !hookChanged {
		plan.Notes = nil
		return plan, nil
	}
	after, err := encodeObject(o)
	if err != nil {
		return plan, err
	}
	plan.Changes = []harness.FileChange{{Path: i.settingsPath(), Before: before, After: string(after)}}
	plan.Notes = append(plan.Notes, "restart running Claude Code sessions to pick up the telemetry settings")
	return plan, nil
}

func isForgeHook(h any) bool {
	o, ok := h.(*object)
	if !ok {
		return false
	}
	cmd, _ := o.get("command")
	return strings.Contains(asString(cmd), hookMarker)
}

// ensureHook makes SessionStart contain exactly one up-to-date forge hook.
func ensureHook(o *object, ep harness.Endpoint) (bool, error) {
	hooks, err := o.childObject("hooks")
	if err != nil {
		return false, err
	}
	groups, _ := hooks.get("SessionStart")
	arr, _ := groups.([]any)
	want := hookCommand(ep)
	found := false
	for _, g := range arr {
		gobj, ok := g.(*object)
		if !ok {
			continue
		}
		hs, _ := gobj.get("hooks")
		harr, _ := hs.([]any)
		for _, h := range harr {
			if isForgeHook(h) {
				cmd, _ := h.(*object).get("command")
				if found || asString(cmd) != want {
					// Duplicate or stale (port change): rebuild from scratch.
					removeForgeHooks(o)
					return addHook(o, want)
				}
				found = true
			}
		}
	}
	if found {
		return false, nil
	}
	return addHook(o, want)
}

func hasHook(o *object, ep harness.Endpoint) bool {
	hv, _ := o.get("hooks")
	hooks, ok := hv.(*object)
	if !ok {
		return false
	}
	groups, _ := hooks.get("SessionStart")
	arr, _ := groups.([]any)
	for _, g := range arr {
		gobj, ok := g.(*object)
		if !ok {
			continue
		}
		hs, _ := gobj.get("hooks")
		harr, _ := hs.([]any)
		for _, h := range harr {
			if hobj, ok := h.(*object); ok && isForgeHook(h) {
				cmd, _ := hobj.get("command")
				if asString(cmd) == hookCommand(ep) {
					return true
				}
			}
		}
	}
	return false
}

func addHook(o *object, cmd string) (bool, error) {
	hooks, err := o.childObject("hooks")
	if err != nil {
		return false, err
	}
	groups, _ := hooks.get("SessionStart")
	arr, _ := groups.([]any)
	h := newObject()
	h.set("type", "command")
	h.set("command", cmd)
	h.set("timeout", 5)
	g := newObject()
	g.set("hooks", []any{h})
	hooks.set("SessionStart", append(arr, g))
	return true, nil
}

// removeForgeHooks drops forge hooks from every event, pruning empty
// groups/events/hooks object. Reports whether something was removed.
func removeForgeHooks(o *object) bool {
	hv, ok := o.get("hooks")
	hooks, isObj := hv.(*object)
	if !ok || !isObj {
		return false
	}
	removed := false
	for _, event := range slices.Clone(hooks.keys) {
		arr, _ := hooks.vals[event].([]any)
		var keptGroups []any
		for _, g := range arr {
			gobj, ok := g.(*object)
			if !ok {
				keptGroups = append(keptGroups, g)
				continue
			}
			hs, _ := gobj.get("hooks")
			harr, _ := hs.([]any)
			var kept []any
			for _, h := range harr {
				if isForgeHook(h) {
					removed = true
					continue
				}
				kept = append(kept, h)
			}
			if len(kept) == 0 && len(harr) > 0 {
				continue
			}
			if len(kept) != len(harr) {
				gobj.set("hooks", kept)
			}
			keptGroups = append(keptGroups, g)
		}
		if len(keptGroups) == 0 && len(arr) > 0 {
			hooks.del(event)
		} else if len(keptGroups) != len(arr) {
			hooks.set(event, keptGroups)
		}
	}
	if len(hooks.keys) == 0 {
		o.del("hooks")
	}
	return removed
}

// PlanRemove undoes PlanSetup.
func (i *Integration) PlanRemove() (harness.Plan, error) {
	plan := harness.Plan{Harness: domain.HarnessClaudeCode}
	before, o, err := i.load()
	if err != nil {
		return plan, err
	}
	changed := removeForgeHooks(o)
	if ev, ok := o.get("env"); ok {
		if env, ok := ev.(*object); ok {
			for _, k := range managedList(env) {
				env.del(k)
				changed = true
			}
			if _, ok := env.get(managedEnvKey); ok {
				env.del(managedEnvKey)
				changed = true
			}
			if len(env.keys) == 0 {
				o.del("env")
			}
		}
	}
	if !changed {
		return plan, nil
	}
	after, err := encodeObject(o)
	if err != nil {
		return plan, err
	}
	plan.Changes = []harness.FileChange{{Path: i.settingsPath(), Before: before, After: string(after)}}
	return plan, nil
}

// Doctor checks the configuration and the capture health.
func (i *Integration) Doctor(ctx context.Context, ep harness.Endpoint) []harness.CheckResult {
	var out []harness.CheckResult
	_, o, err := i.load()
	if err != nil {
		return []harness.CheckResult{{Name: "Claude Code settings", OK: false, Detail: err.Error(), Fix: "fix the JSON syntax of " + i.settingsPath()}}
	}
	env, _ := o.childObject("env")
	envOK := env != nil
	if envOK {
		for _, d := range desiredEnv(ep) {
			v, _ := env.get(d.k)
			if asString(v) != d.v {
				envOK = false
				break
			}
		}
	}
	out = append(out, check("Telemetry export to daemon", envOK, i.settingsPath(), "run `forge setup`"))

	out = append(out, check("SessionStart hook", hasHook(o, ep), "posts the session cwd to the daemon", "run `forge setup`"))
	_, curlErr := exec.LookPath("curl")
	out = append(out, check("curl available (used by the hook)", curlErr == nil, "", "install curl"))
	if v := os.Getenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"); v != "" && v != ep.BaseURL+"/v1/logs" {
		out = append(out, check("Shell environment", false, "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT="+v+" overrides settings.json", "unset it in your shell profile"))
	}
	return append(out, daemonprobe.Checks(ctx, ep, domain.HarnessClaudeCode, "restart Claude Code sessions after `forge setup` and send a prompt")...)
}

func check(name string, ok bool, detail, fix string) harness.CheckResult {
	r := harness.CheckResult{Name: name, OK: ok, Detail: detail}
	if !ok {
		r.Fix = fix
	}
	return r
}

var _ harness.Integration = (*Integration)(nil)
