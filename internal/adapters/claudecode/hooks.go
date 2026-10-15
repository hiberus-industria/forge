package claudecode

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/hiberus-industria/forge/internal/domain"
	"github.com/hiberus-industria/forge/internal/harness"
)

// HookPayload holds the hook input fields forge reads. Everything else
// (prompts, transcript content) is ignored on purpose.
type HookPayload struct {
	SessionID     string `json:"session_id"`
	Cwd           string `json:"cwd"`
	HookEventName string `json:"hook_event_name"`
}

// ParseHook decodes a hook payload into a session announcement.
func ParseHook(b []byte, now time.Time) (harness.SessionStarted, bool, error) {
	var p HookPayload
	if err := json.Unmarshal(b, &p); err != nil {
		return harness.SessionStarted{}, false, err
	}
	if p.SessionID == "" || p.Cwd == "" {
		return harness.SessionStarted{}, false, nil
	}
	return harness.SessionStarted{Harness: domain.HarnessClaudeCode, SessionID: p.SessionID, Cwd: p.Cwd, At: now}, true, nil
}

// HookHandler serves POST /hooks/claude-code. It answers 204 with an empty
// body so that Claude Code adds nothing to the conversation context.
func HookHandler(sink harness.Sink, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		s, ok, err := ParseHook(b, time.Now().UTC())
		if err != nil {
			http.Error(w, "invalid hook payload", http.StatusBadRequest)
			return
		}
		if ok {
			if err := sink.SessionStarted(r.Context(), s); err != nil {
				log.Error("claude-code: session hook failed", "err", err)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
