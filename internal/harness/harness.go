// Package harness declares how forge plugs into AI coding harnesses on the
// developer machine. Each harness adapter (adapters/claudecode,
// adapters/opencode) implements Integration for `forge setup`/`forge doctor`
// and feeds captured requests to the daemon as Captured values.
package harness

import (
	"context"
	"time"

	"github.com/hiberus-industria/forge/internal/domain"
)

// Captured is a usage record as observed on the developer machine, before
// attribution. Cwd is used locally to resolve repo/branch and is never sent.
type Captured struct {
	Harness      domain.Harness
	ExternalID   string
	SessionID    string
	Provider     string
	Model        string
	Tokens       domain.Tokens
	OccurredAt   time.Time
	ReportedCost domain.Micros
	// Cwd is the working directory of the session, when known by the source.
	// When empty the daemon resolves it from SessionID (see SessionStarted).
	Cwd string
}

// SessionStarted announces a harness session and its working directory
// (Claude Code SessionStart hook, OpenCode plugin context).
type SessionStarted struct {
	Harness   domain.Harness
	SessionID string
	Cwd       string
	At        time.Time
}

// Sink receives what harness adapters capture. Implemented by the daemon.
type Sink interface {
	Capture(ctx context.Context, c Captured) error
	SessionStarted(ctx context.Context, s SessionStarted) error
}

// Endpoint is how the daemon is reachable by harnesses on this machine.
type Endpoint struct {
	// BaseURL of the daemon's loopback ingest listener, e.g. http://127.0.0.1:47318
	BaseURL string
}

// FileChange is one file modified by Setup, rendered as a diff before applying.
type FileChange struct {
	Path   string
	Before string // empty when the file does not exist
	After  string
}

// Plan is the set of changes needed to wire a harness to the daemon.
// An empty plan means the harness is already configured (idempotency).
type Plan struct {
	Harness domain.Harness
	Changes []FileChange
	Notes   []string // e.g. "restart running Claude Code sessions"
}

// CheckResult is a `forge doctor` diagnostic line.
type CheckResult struct {
	Name   string
	OK     bool
	Detail string
	Fix    string // suggested remediation when !OK
}

// Integration wires one harness to forge.
type Integration interface {
	Harness() domain.Harness
	// Installed reports whether the harness binary/config is present.
	Installed() bool
	// PlanSetup computes the changes to apply (pure: does not write).
	PlanSetup(ep Endpoint) (Plan, error)
	// PlanRemove computes the changes that undo PlanSetup.
	PlanRemove() (Plan, error)
	// Doctor checks configuration and recent capture health.
	Doctor(ctx context.Context, ep Endpoint) []CheckResult
}

// Apply writes a plan's changes, keeping a timestamped backup of every
// modified file next to it (<file>.forge-bak-<unix>).
func Apply(p Plan) error { return applyPlan(p) }
