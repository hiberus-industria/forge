package domain

import (
	"errors"
	"fmt"
	"time"
)

// Harness identifies the AI coding tool that produced a usage event.
type Harness string

const (
	HarnessClaudeCode Harness = "claude-code"
	HarnessOpenCode   Harness = "opencode"
)

// Valid reports whether the harness is one forge knows about.
func (h Harness) Valid() bool { return h == HarnessClaudeCode || h == HarnessOpenCode }

// Tokens is the token accounting of a single model request.
type Tokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	Reasoning  int64 `json:"reasoning"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

// Total returns the sum of every token kind.
func (t Tokens) Total() int64 {
	return t.Input + t.Output + t.Reasoning + t.CacheRead + t.CacheWrite
}

// Add returns the element-wise sum of two token counts.
func (t Tokens) Add(o Tokens) Tokens {
	return Tokens{
		Input:      t.Input + o.Input,
		Output:     t.Output + o.Output,
		Reasoning:  t.Reasoning + o.Reasoning,
		CacheRead:  t.CacheRead + o.CacheRead,
		CacheWrite: t.CacheWrite + o.CacheWrite,
	}
}

// UsageEvent is one model request made by a developer through a harness.
//
// It only carries metadata: never prompts, completions, code or paths outside
// the repository. Tokens are the source of truth; cost is derived from the
// price book, and ReportedCost is what the harness itself estimated (kept for
// information only).
type UsageEvent struct {
	// ExternalID is the harness-native identifier of the request
	// (Claude Code request_id / message id, OpenCode message id).
	ExternalID string
	Harness    Harness
	SessionID  string
	Provider   string
	Model      string
	Tokens     Tokens
	OccurredAt time.Time
	// ReportedCost is the harness' own estimate; zero when unknown.
	ReportedCost Micros
	// MachineID identifies the developer machine whose daemon captured it.
	MachineID string
	// Attribution is resolved on the developer machine at capture time.
	Attribution Attribution
}

// DedupKey is the idempotency key of the event across retries and re-syncs.
func (e UsageEvent) DedupKey() string {
	return fmt.Sprintf("%s:%s", e.Harness, e.ExternalID)
}

// Validation errors.
var (
	ErrInvalidHarness  = errors.New("invalid harness")
	ErrMissingID       = errors.New("missing external id")
	ErrMissingModel    = errors.New("missing model")
	ErrNegativeTokens  = errors.New("negative token count")
	ErrMissingOccurred = errors.New("missing occurred_at")
)

// Validate checks the invariants of a usage event.
func (e UsageEvent) Validate() error {
	switch {
	case !e.Harness.Valid():
		return ErrInvalidHarness
	case e.ExternalID == "":
		return ErrMissingID
	case e.Model == "":
		return ErrMissingModel
	case e.OccurredAt.IsZero():
		return ErrMissingOccurred
	}
	t := e.Tokens
	if t.Input < 0 || t.Output < 0 || t.Reasoning < 0 || t.CacheRead < 0 || t.CacheWrite < 0 {
		return ErrNegativeTokens
	}
	return e.Attribution.Validate()
}
