package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/app"
	"github.com/hiberus-industria/forge/internal/domain"
)

// fakeStore implements only what these tests touch; other methods panic
// through the nil embedded interface.
type fakeStore struct {
	app.Store
	user     app.User
	inserted []app.NewEvent
	pingErr  error
}

func (f *fakeStore) Ping(context.Context) error { return f.pingErr }
func (f *fakeStore) UpsertUser(_ context.Context, sub, email, name string) (app.User, error) {
	f.user = app.User{ID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), Subject: sub, Email: email, Name: name}
	return f.user, nil
}

func (f *fakeStore) ListPrices(context.Context, string) ([]domain.Price, error) {
	return []domain.Price{{ID: 1, Model: "m", Input: 1, EffectiveFrom: time.Unix(0, 0)}}, nil
}
func (f *fakeStore) EnsureIssues(context.Context, []domain.IssueRef) error { return nil }
func (f *fakeStore) InsertEvents(_ context.Context, evs []app.NewEvent) (int, error) {
	f.inserted = append(f.inserted, evs...)
	return len(evs), nil
}

func (f *fakeStore) GetRepo(context.Context, domain.RepoRef) (app.Repo, error) {
	return app.Repo{}, app.ErrNotFound
}

type fakeVerifier struct{}

func (fakeVerifier) Verify(_ context.Context, raw string) (app.Principal, error) {
	switch raw {
	case "member":
		return app.Principal{Subject: "s1", Email: "dev@example.com", Name: "Dev"}, nil
	case "admin":
		return app.Principal{Subject: "s2", Name: "Admin", Admin: true}, nil
	}
	return app.Principal{}, app.ErrUnauthorized
}

type fakeWebhooks struct{ err error }

func (f fakeWebhooks) Parse(*http.Request) ([]app.GitHubEvent, error) { return nil, f.err }

func newServer(store *fakeStore, wh app.WebhookParser) http.Handler {
	svc := app.New(app.Deps{Store: store, Org: "hiberus-industria"})
	return New(Options{Service: svc, Verifier: fakeVerifier{}, Webhooks: wh})
}

func do(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeErr(t *testing.T, rec *httptest.ResponseRecorder) api.Error {
	t.Helper()
	var e api.Error
	if err := json.NewDecoder(rec.Body).Decode(&e); err != nil {
		t.Fatalf("error body: %v", err)
	}
	return e
}

func TestAuth(t *testing.T) {
	h := newServer(&fakeStore{}, nil)
	for _, tok := range []string{"", "garbage"} {
		rec := do(t, h, http.MethodGet, "/v1/me", tok, "")
		if rec.Code != http.StatusUnauthorized || decodeErr(t, rec).Code != "unauthorized" {
			t.Fatalf("token %q: status %d", tok, rec.Code)
		}
		if rec.Header().Get("WWW-Authenticate") == "" {
			t.Fatal("missing WWW-Authenticate")
		}
	}
	rec := do(t, h, http.MethodGet, "/v1/me", "admin", "")
	var u api.User
	if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&u) != nil || len(u.Roles) != 2 {
		t.Fatalf("me: %d %+v", rec.Code, u)
	}
}

func TestIngest(t *testing.T) {
	store := &fakeStore{}
	h := newServer(store, nil)
	ev := `{"external_id":"r1","harness":"claude-code","session_id":"s","model":"m",
		"tokens":{"input":1000000,"output":0,"reasoning":0,"cache_read":0,"cache_write":0},
		"occurred_at":"2026-10-01T10:00:00Z","machine_id":"m1",
		"attribution":{"bucket":"issue","source":"branch","repo":"hiberus-industria/api","branch":"feat/1-x","issue":{"repo":"hiberus-industria/api","number":1}}%s}`

	rec := do(t, h, http.MethodPost, "/v1/usage-events/batch", "member", `{"events":[`+strings.Replace(ev, "%s", "", 1)+`]}`)
	var res api.IngestResult
	if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&res) != nil || res.Accepted != 1 || res.Rejected == nil {
		t.Fatalf("ingest: %d %s", rec.Code, rec.Body)
	}
	if len(store.inserted) != 1 || store.inserted[0].Cost != 1_000_000 || store.inserted[0].UserID != store.user.ID {
		t.Fatalf("inserted %+v", store.inserted)
	}

	// Privacy: unknown fields (e.g. a prompt) are rejected outright.
	rec = do(t, h, http.MethodPost, "/v1/usage-events/batch", "member", `{"events":[`+strings.Replace(ev, "%s", `,"prompt":"secret"`, 1)+`]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field accepted: %d", rec.Code)
	}

	rec = do(t, h, http.MethodPost, "/v1/usage-events/batch", "member", `{"events":[{"external_id":"x","harness":"cursor","session_id":"s","model":"m","tokens":{"input":0,"output":0,"reasoning":0,"cache_read":0,"cache_write":0},"occurred_at":"2026-10-01T10:00:00Z","machine_id":"m","attribution":{"bucket":"external","source":"none"}}]}`)
	if json.NewDecoder(rec.Body).Decode(&res) != nil || len(res.Rejected) != 1 {
		t.Fatalf("invalid harness should be rejected per event: %s", rec.Body)
	}
}

func TestErrorsAndProbes(t *testing.T) {
	store := &fakeStore{}
	h := newServer(store, nil)
	if rec := do(t, h, http.MethodGet, "/v1/repos/hiberus-industria/nope", "member", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("not found: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/v1/reports/costs", "member", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing group_by: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/v1/prices/sync", "member", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("member price sync: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/healthz", "", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("healthz: %d", rec.Code)
	}
	store.pingErr = errors.New("down")
	if rec := do(t, h, http.MethodGet, "/readyz", "", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz: %d", rec.Code)
	}
}

func TestWebhook(t *testing.T) {
	if rec := do(t, newServer(&fakeStore{}, nil), http.MethodPost, "/webhooks/github", "", "{}"); rec.Code != http.StatusNotImplemented {
		t.Fatalf("no parser: %d", rec.Code)
	}
	bad := newServer(&fakeStore{}, fakeWebhooks{err: app.ErrUnauthorized})
	if rec := do(t, bad, http.MethodPost, "/webhooks/github", "", "{}"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature: %d", rec.Code)
	}
	ok := newServer(&fakeStore{}, fakeWebhooks{})
	if rec := do(t, ok, http.MethodPost, "/webhooks/github", "", "{}"); rec.Code != http.StatusAccepted {
		t.Fatalf("valid delivery: %d", rec.Code)
	}
}
