// Package httpapi exposes the use cases over HTTP: the OpenAPI contract
// (generated strict server), the GitHub webhook and health probes.
package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/app"
)

// Options configures the HTTP handler.
type Options struct {
	Service  *app.Service
	Verifier app.TokenVerifier
	// Webhooks may be nil when the GitHub App is not configured.
	Webhooks app.WebhookParser
	Logger   *slog.Logger
}

// maxBody bounds request bodies (a full ingest batch is well below it).
const maxBody = 4 << 20

// New builds the root handler.
func New(o Options) http.Handler {
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	strict := api.NewStrictHandlerWithOptions(&handlers{svc: o.Service}, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, log, errors.Join(app.ErrInvalid, err))
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, log, err)
		},
	})
	apiMux := http.NewServeMux()
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{
		BaseRouter: apiMux,
		ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, log, errors.Join(app.ErrInvalid, err))
		},
	})

	mux := http.NewServeMux()
	mux.Handle("/v1/", authenticate(o.Verifier, log, strictIngest(apiMux)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := o.Service.Ping(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /webhooks/github", webhookHandler(o.Service, o.Webhooks, log))
	return recoverer(log, logRequests(log, limitBody(mux)))
}

func webhookHandler(svc *app.Service, parser app.WebhookParser, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if parser == nil {
			http.Error(w, "github app not configured", http.StatusNotImplemented)
			return
		}
		evs, err := parser.Parse(r)
		if err != nil {
			log.Warn("rejected github webhook", "delivery", r.Header.Get("X-GitHub-Delivery"), "err", err)
			http.Error(w, "invalid webhook", http.StatusUnauthorized)
			return
		}
		if err := svc.HandleGitHubEvents(r.Context(), evs); err != nil {
			// GitHub retries are not useful for partial failures: the
			// reconciler is the safety net. Log and acknowledge.
			log.Warn("github webhook handling", "delivery", r.Header.Get("X-GitHub-Delivery"), "err", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}
}

// strictIngest rejects ingest payloads with unknown fields: daemons must
// only send the metadata defined by the contract (privacy by design).
func strictIngest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/usage-events/batch" {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, nil, errors.Join(app.ErrInvalid, err))
			return
		}
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		var probe api.IngestRequest
		if err := dec.Decode(&probe); err != nil {
			writeError(w, nil, errors.Join(app.ErrInvalid, err))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}

func authenticate(v app.TokenVerifier, log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || strings.TrimSpace(raw) == "" {
			writeError(w, log, app.ErrUnauthorized)
			return
		}
		p, err := v.Verify(r.Context(), strings.TrimSpace(raw))
		if err != nil {
			log.Debug("token rejected", "err", err)
			writeError(w, log, app.ErrUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(app.WithPrincipal(r.Context(), p)))
	})
}

func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			return
		}
		log.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.status, "duration", time.Since(start))
	})
}

func recoverer(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Error("panic", "path", r.URL.Path, "panic", v)
				writeError(w, nil, errors.New("internal error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// writeError maps application errors to HTTP responses.
func writeError(w http.ResponseWriter, log *slog.Logger, err error) {
	status, code := http.StatusInternalServerError, "internal"
	switch {
	case errors.Is(err, app.ErrUnauthorized):
		status, code = http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, app.ErrForbidden):
		status, code = http.StatusForbidden, "forbidden"
	case errors.Is(err, app.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, app.ErrConflict):
		status, code = http.StatusConflict, "conflict"
	case errors.Is(err, app.ErrInvalid):
		status, code = http.StatusBadRequest, "invalid"
	case errors.Is(err, app.ErrUnavailable):
		status, code = http.StatusServiceUnavailable, "unavailable"
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		status, code = http.StatusRequestEntityTooLarge, "too_large"
	}
	msg := err.Error()
	if status == http.StatusInternalServerError {
		if log != nil {
			log.Error("request failed", "err", err)
		}
		msg = "internal server error"
	}
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="forge"`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(api.Error{Code: code, Message: msg})
}
