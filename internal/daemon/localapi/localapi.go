// Package localapi is the contract between the forge daemon and local clients
// (TUI, CLI commands). It is HTTP/JSON over a unix socket readable only by
// the developer (0600), plus Server-Sent Events for the live feed.
//
// Routes:
//
//	GET    /v1/status             -> Status
//	GET    /v1/token              -> auth.Token  (access token for the forge API)
//	POST   /v1/credentials/reload -> 204         (after `forge login`/`logout`)
//	GET    /v1/live/recent?limit= -> []LiveEvent (newest first, in-memory ring)
//	GET    /v1/live               -> text/event-stream of LiveEvent (event: usage)
//	GET    /v1/override           -> Override | 204 when none
//	PUT    /v1/override           <- OverrideRequest -> Override
//	DELETE /v1/override           -> 204
//	POST   /v1/sync               -> SyncResult  (flush the buffer now)
package localapi

import (
	"time"

	"github.com/hiberus-industria/forge/internal/domain"
)

// Status is the daemon health as shown by the TUI header and `forge status`.
type Status struct {
	Version   string    `json:"version"`
	MachineID string    `json:"machine_id"`
	StartedAt time.Time `json:"started_at"`
	APIURL    string    `json:"api_url"`
	Org       string    `json:"org"`
	// IngestURL is the loopback listener harnesses send telemetry to.
	IngestURL string `json:"ingest_url"`

	LoggedIn bool      `json:"logged_in"`
	User     *UserInfo `json:"user,omitempty"`

	// Pending is the number of captured events not yet accepted by the API.
	Pending       int        `json:"pending"`
	LastSyncAt    *time.Time `json:"last_sync_at,omitempty"`
	LastSyncError string     `json:"last_sync_error,omitempty"`

	Captures []CaptureStatus `json:"captures"`
	Override *Override       `json:"override,omitempty"`
}

// UserInfo identifies the logged-in developer.
type UserInfo struct {
	Name        string `json:"name"`
	Email       string `json:"email"`
	GitHubLogin string `json:"github_login,omitempty"`
}

// CaptureStatus summarizes one harness capture channel.
type CaptureStatus struct {
	Harness     domain.Harness `json:"harness"`
	LastEventAt *time.Time     `json:"last_event_at,omitempty"`
	EventsToday int            `json:"events_today"`
}

// LiveEvent is a captured request after attribution, with a local cost
// estimate (daemon-cached price book, falling back to the harness estimate).
type LiveEvent struct {
	Harness     domain.Harness     `json:"harness"`
	SessionID   string             `json:"session_id"`
	Provider    string             `json:"provider,omitempty"`
	Model       string             `json:"model"`
	Tokens      domain.Tokens      `json:"tokens"`
	CostMicros  domain.Micros      `json:"cost_usd_micros"`
	OccurredAt  time.Time          `json:"occurred_at"`
	Attribution domain.Attribution `json:"attribution"`
	Synced      bool               `json:"synced"`
}

// OverrideScope limits where a manual task override applies.
type OverrideScope string

const (
	// ScopeRepo applies to sessions whose working directory is inside RepoPath.
	ScopeRepo OverrideScope = "repo"
	// ScopeGlobal applies to every session on the machine.
	ScopeGlobal OverrideScope = "global"
)

// Override is a task forced by the developer (attribution source "manual").
type Override struct {
	Issue    domain.IssueRef `json:"issue"`
	Scope    OverrideScope   `json:"scope"`
	RepoPath string          `json:"repo_path,omitempty"` // absolute git top-level, for ScopeRepo
	SetAt    time.Time       `json:"set_at"`
}

// OverrideRequest sets the override.
type OverrideRequest struct {
	Issue    domain.IssueRef `json:"issue"`
	Scope    OverrideScope   `json:"scope"`
	RepoPath string          `json:"repo_path,omitempty"`
}

// SyncResult reports a manual flush.
type SyncResult struct {
	Sent       int `json:"sent"`
	Duplicates int `json:"duplicates"`
	Rejected   int `json:"rejected"`
	Pending    int `json:"pending"`
}

// Error is the JSON body of non-2xx responses.
type Error struct {
	Message string `json:"message"`
}
