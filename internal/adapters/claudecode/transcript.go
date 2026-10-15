package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/hiberus-industria/forge/internal/domain"
	"github.com/hiberus-industria/forge/internal/harness"
)

// ConfigDir is Claude Code's configuration directory (CLAUDE_CONFIG_DIR or ~/.claude).
func ConfigDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".claude")
}

// ProjectsDir holds the session transcripts.
func ProjectsDir() string { return filepath.Join(ConfigDir(), "projects") }

// transcriptLine is the subset of a transcript JSONL line forge reads. The
// format is undocumented and may change; every field is optional.
type transcriptLine struct {
	Type      string    `json:"type"`
	SessionID string    `json:"sessionId"`
	Cwd       string    `json:"cwd"`
	RequestID string    `json:"requestId"`
	Timestamp time.Time `json:"timestamp"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// SessionCwd finds the working directory of a session from its transcript
// (<projects>/<encoded-cwd>/<session>.jsonl). Used when the SessionStart
// hook did not reach the daemon (daemon restarted, hook not installed).
func SessionCwd(projectsDir, sessionID string) string {
	if sessionID == "" || filepath.Base(sessionID) != sessionID {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(projectsDir, "*", sessionID+".jsonl"))
	for _, m := range matches {
		f, err := os.Open(m)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for i := 0; sc.Scan() && i < 200; i++ {
			var l transcriptLine
			if json.Unmarshal(sc.Bytes(), &l) == nil && l.Cwd != "" {
				f.Close()
				return l.Cwd
			}
		}
		f.Close()
	}
	return ""
}

// Reconcile replays assistant messages from transcripts modified since the
// given instant, so requests missed by OTLP (telemetry off, daemon down) are
// still captured. ExternalID is the API requestId, the same id the OTLP
// event carries, so the daemon's dedup makes replays harmless. One response
// is written as several lines (one per content block) repeating the same
// usage, hence the per-message dedup. Cost is left to the price book.
func Reconcile(ctx context.Context, projectsDir string, since time.Time, sink harness.Sink) (int, error) {
	n := 0
	err := filepath.WalkDir(projectsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".jsonl" || ctx.Err() != nil {
			return ctx.Err()
		}
		if info, err := d.Info(); err != nil || info.ModTime().Before(since) {
			return nil
		}
		c, err := reconcileFile(ctx, path, since, sink)
		n += c
		return err
	})
	return n, err
}

func reconcileFile(ctx context.Context, path string, since time.Time, sink harness.Sink) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, nil
	}
	defer f.Close()
	seen := map[string]bool{}
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var l transcriptLine
		if json.Unmarshal(sc.Bytes(), &l) != nil || l.Type != "assistant" || l.RequestID == "" || l.Message.Model == "" {
			continue
		}
		if l.Timestamp.Before(since) || l.Message.Model == "<synthetic>" {
			continue
		}
		key := l.Message.ID + "|" + l.RequestID
		if seen[key] {
			continue
		}
		seen[key] = true
		u := l.Message.Usage
		c := harness.Captured{
			Harness: domain.HarnessClaudeCode, ExternalID: l.RequestID, SessionID: l.SessionID,
			Provider: "anthropic", Model: l.Message.Model, OccurredAt: l.Timestamp.UTC(), Cwd: l.Cwd,
			Tokens: domain.Tokens{Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheReadInputTokens, CacheWrite: u.CacheCreationInputTokens},
		}
		if err := sink.Capture(ctx, c); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
