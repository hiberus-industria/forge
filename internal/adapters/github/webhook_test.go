package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hiberus-industria/forge/internal/app"
	"github.com/hiberus-industria/forge/internal/domain"
)

var secret = []byte("s3cret")

func delivery(t *testing.T, event, body string, sign []byte) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-GitHub-Event", event)
	if sign != nil {
		mac := hmac.New(sha256.New, sign)
		mac.Write([]byte(body))
		r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	return r
}

const repoJSON = `"repository":{"name":"api","full_name":"hiberus-industria/api","owner":{"login":"hiberus-industria"}}`

func parse(t *testing.T, event, body string) []app.GitHubEvent {
	t.Helper()
	evs, err := NewWebhookParser(secret, org).Parse(delivery(t, event, body, secret))
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestWebhookSignature(t *testing.T) {
	p := NewWebhookParser(secret, org)
	body := `{"action":"opened"}`
	if _, err := p.Parse(delivery(t, "issues", body, []byte("wrong"))); !errors.Is(err, app.ErrUnauthorized) {
		t.Fatalf("wrong signature must fail, got %v", err)
	}
	if _, err := p.Parse(delivery(t, "issues", body, nil)); !errors.Is(err, app.ErrUnauthorized) {
		t.Fatalf("missing signature must fail, got %v", err)
	}
}

func TestWebhookIssues(t *testing.T) {
	cases := map[string]app.GitHubEventKind{
		"opened": app.GHIssueUpserted, "labeled": app.GHIssueUpserted, "assigned": app.GHIssueUpserted,
		"closed": app.GHIssueClosed, "reopened": app.GHIssueReopened,
	}
	for action, kind := range cases {
		evs := parse(t, "issues", `{"action":"`+action+`",`+repoJSON+`,
			"issue":{"number":5,"node_id":"I5","title":"T","state":"open","labels":[{"name":"bug"}],"updated_at":"2026-10-01T10:00:00Z"}}`)
		if len(evs) != 1 || evs[0].Kind != kind || evs[0].Issue == nil || evs[0].Issue.Ref.Number != 5 ||
			evs[0].Repo != repo || evs[0].At.IsZero() {
			t.Fatalf("%s: %+v", action, evs)
		}
	}
	if evs := parse(t, "issues", `{"action":"milestoned",`+repoJSON+`,"issue":{"number":5}}`); len(evs) != 0 {
		t.Fatalf("irrelevant action produced %+v", evs)
	}
	other := `{"action":"opened","repository":{"name":"x","owner":{"login":"other-org"}},"issue":{"number":1}}`
	if evs := parse(t, "issues", other); len(evs) != 0 {
		t.Fatalf("other org produced %+v", evs)
	}
}

func TestWebhookPullRequest(t *testing.T) {
	evs := parse(t, "pull_request", `{"action":"opened",`+repoJSON+`,
		"pull_request":{"number":40,"head":{"ref":"feat/12-login"},"body":"Closes #12, fixes #7 and resolves other/repo#9. Fixes hiberus-industria/api#8","updated_at":"2026-10-01T10:00:00Z"}}`)
	if len(evs) != 1 || evs[0].Kind != app.GHPROpened || evs[0].PRNumber != 40 {
		t.Fatalf("%+v", evs)
	}
	if want := []int{12, 7, 8}; !reflect.DeepEqual(evs[0].LinkedIssues, want) {
		t.Fatalf("linked = %v, want %v", evs[0].LinkedIssues, want)
	}

	merged := parse(t, "pull_request", `{"action":"closed",`+repoJSON+`,
		"pull_request":{"number":40,"merged":true,"merged_at":"2026-10-02T09:00:00Z","head":{"ref":"fix/3"}}}`)
	if len(merged) != 1 || merged[0].Kind != app.GHPRMerged || merged[0].At.Day() != 2 || merged[0].LinkedIssues[0] != 3 {
		t.Fatalf("%+v", merged)
	}
	if evs := parse(t, "pull_request", `{"action":"closed",`+repoJSON+`,"pull_request":{"number":40,"merged":false}}`); len(evs) != 0 {
		t.Fatalf("closed without merge produced %+v", evs)
	}
}

func TestWebhookRepoChanges(t *testing.T) {
	evs := parse(t, "repository", `{"action":"renamed",`+repoJSON+`}`)
	if len(evs) != 1 || evs[0].Kind != app.GHRepoChanged || evs[0].Repo != repo {
		t.Fatalf("%+v", evs)
	}
	evs = parse(t, "installation_repositories", `{"action":"added",
		"repositories_added":[{"full_name":"hiberus-industria/web"},{"full_name":"other/x"}]}`)
	if len(evs) != 1 || evs[0].Repo != (domain.RepoRef{Owner: org, Name: "web"}) {
		t.Fatalf("%+v", evs)
	}
	evs = parse(t, "projects_v2_item", `{"action":"edited","organization":{"login":"hiberus-industria"},"projects_v2_item":{"id":1}}`)
	if len(evs) != 1 || evs[0].Kind != app.GHRepoChanged || evs[0].Repo.Name != "" {
		t.Fatalf("%+v", evs)
	}
	if evs := parse(t, "ping", `{"zen":"hi"}`); len(evs) != 0 {
		t.Fatalf("ping produced %+v", evs)
	}
}
