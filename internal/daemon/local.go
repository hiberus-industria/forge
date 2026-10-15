package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/hiberus-industria/forge/internal/auth"
	"github.com/hiberus-industria/forge/internal/daemon/localapi"
	"github.com/hiberus-industria/forge/internal/domain"
)

// listenSocket opens the unix socket readable only by the developer,
// refusing to start when another daemon already answers on it.
func listenSocket(path string) (net.Listener, error) {
	// sun_path is 104 bytes on macOS and 108 on Linux, including the NUL.
	if len(path) > 103 {
		return nil, fmt.Errorf("socket path too long (%d bytes, max 103): %s; set FORGE_SOCKET to a shorter path", len(path), path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			c.Close()
			return nil, fmt.Errorf("another forge daemon is already listening on %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale socket: %w", err)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, localapi.Error{Message: err.Error()})
}

// localHandler implements the localapi contract.
func (d *Daemon) localHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		st, err := d.status(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, st)
	})
	mux.HandleFunc("GET /v1/token", func(w http.ResponseWriter, r *http.Request) {
		t, err := d.session.Token(r.Context())
		switch {
		case errors.Is(err, auth.ErrNotLoggedIn):
			writeErr(w, http.StatusUnauthorized, err)
		case err != nil:
			writeErr(w, http.StatusBadGateway, err)
		default:
			writeJSON(w, http.StatusOK, t)
		}
	})
	mux.HandleFunc("POST /v1/credentials/reload", func(w http.ResponseWriter, r *http.Request) {
		if err := d.session.Reload(r.Context()); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		d.afterLogin()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/live/recent", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 200 {
			limit = 50
		}
		evs, err := d.recent(r.Context(), limit)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, evs)
	})
	mux.HandleFunc("GET /v1/live", d.serveLive)
	mux.HandleFunc("GET /v1/override", func(w http.ResponseWriter, r *http.Request) {
		if o := d.svc.currentOverride(); o != nil {
			writeJSON(w, http.StatusOK, o)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PUT /v1/override", func(w http.ResponseWriter, r *http.Request) {
		var req localapi.OverrideRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		o, err := d.svc.setOverride(r.Context(), req)
		switch {
		case errors.Is(err, errBadOverride):
			writeErr(w, http.StatusBadRequest, err)
		case err != nil:
			writeErr(w, http.StatusInternalServerError, err)
		default:
			writeJSON(w, http.StatusOK, o)
		}
	})
	mux.HandleFunc("DELETE /v1/override", func(w http.ResponseWriter, r *http.Request) {
		if err := d.svc.clearOverride(r.Context()); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/sync", func(w http.ResponseWriter, r *http.Request) {
		res, err := d.sync.flush(r.Context())
		switch {
		case errors.Is(err, auth.ErrNotLoggedIn):
			writeErr(w, http.StatusUnauthorized, err)
		case err != nil:
			writeErr(w, http.StatusBadGateway, err)
		default:
			writeJSON(w, http.StatusOK, res)
		}
	})
	return mux
}

func (d *Daemon) serveLive(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}
	ch, cancel := d.hub.subscribe()
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-d.done:
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
		case ev := <-ch:
			b, _ := json.Marshal(ev)
			fmt.Fprintf(w, "event: usage\ndata: %s\n\n", b)
		}
		fl.Flush()
	}
}

func (d *Daemon) recent(ctx context.Context, limit int) ([]localapi.LiveEvent, error) {
	stored, err := d.store.Recent(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]localapi.LiveEvent, len(stored))
	for i, se := range stored {
		e := se.Event
		out[i] = localapi.LiveEvent{
			Harness: e.Harness, SessionID: e.SessionID, Provider: e.Provider, Model: e.Model,
			Tokens: e.Tokens, CostMicros: se.Estimate, OccurredAt: e.OccurredAt,
			Attribution: e.Attribution, Synced: se.Status == "synced",
		}
	}
	return out, nil
}

func (d *Daemon) status(ctx context.Context) (localapi.Status, error) {
	st := localapi.Status{
		Version: d.version, MachineID: d.machineID, StartedAt: d.startedAt,
		APIURL: d.cfg.APIURL, Org: d.cfg.Org, IngestURL: d.cfg.IngestURL(),
		Override: d.svc.currentOverride(),
		Captures: []localapi.CaptureStatus{},
	}
	if id, ok := d.session.Identity(); ok {
		st.LoggedIn = true
		st.User = &localapi.UserInfo{Name: id.Name, Email: id.Email, GitHubLogin: d.githubLogin()}
	}
	var err error
	if st.Pending, err = d.store.CountPending(ctx); err != nil {
		return st, err
	}
	st.LastSyncAt, st.LastSyncError = d.sync.state()

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	stats, err := d.store.Stats(ctx, today)
	if err != nil {
		return st, err
	}
	seen := map[domain.Harness]bool{}
	for _, s := range stats {
		last := s.LastEventAt
		st.Captures = append(st.Captures, localapi.CaptureStatus{Harness: s.Harness, LastEventAt: &last, EventsToday: s.EventsSince})
		seen[s.Harness] = true
	}
	for _, in := range d.integrations {
		if !seen[in.Harness()] && in.Installed() {
			st.Captures = append(st.Captures, localapi.CaptureStatus{Harness: in.Harness()})
		}
	}
	return st, nil
}
