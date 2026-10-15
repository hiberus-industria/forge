package domain

import (
	"testing"
	"time"
)

var org = "hiberus-industria"

func repo(name string) *RepoRef { return &RepoRef{Owner: org, Name: name} }

func TestParseBranch(t *testing.T) {
	cases := []struct {
		in    string
		issue int
		typ   BranchType
		ok    bool
	}{
		{"feat/123-login-oidc", 123, BranchFeat, true},
		{"fix/88-null-cost", 88, BranchFix, true},
		{"refs/heads/docs/7", 7, BranchDocs, true},
		{"42-gh-issue-develop", 42, "", true},
		{"main", 0, "", false},
		{"feat/login", 0, "", false},
		{"release/1.2.0", 0, "", false},
		{"feat/0-zero", 0, "", false},
	}
	for _, c := range cases {
		b, ok := ParseBranch(c.in)
		if ok != c.ok || b.Issue != c.issue || b.Type != c.typ {
			t.Errorf("ParseBranch(%q) = %+v,%v; want issue=%d type=%q ok=%v", c.in, b, ok, c.issue, c.typ, c.ok)
		}
	}
}

func TestBranchName(t *testing.T) {
	got := BranchName(BranchFix, 12, "Corregir cálculo de coste en caché (1h)")
	if want := "fix/12-corregir-calculo-de-coste-en-cache-1h"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if b, ok := ParseBranch(got); !ok || b.Issue != 12 {
		t.Fatalf("generated branch must round-trip, got %+v", b)
	}
	long := BranchName(BranchFeat, 1, "A very long issue title that keeps going and going beyond any reasonable size")
	if len(long) > len("feat/1-")+40 {
		t.Fatalf("slug not truncated: %q", long)
	}
}

func TestAttribute(t *testing.T) {
	ov := IssueRef{Repo: *repo("api"), Number: 9}
	cases := []struct {
		name   string
		in     AttributionInput
		bucket Bucket
		source AttributionSource
		issue  int
	}{
		{"branch", AttributionInput{Org: org, Repo: repo("api"), Branch: "feat/5-x"}, BucketIssue, SourceBranch, 5},
		{"default branch", AttributionInput{Org: org, Repo: repo("api"), Branch: "main"}, BucketRepo, SourceNone, 0},
		{"org case-insensitive", AttributionInput{Org: org, Repo: &RepoRef{Owner: "Hiberus-Industria", Name: "api"}, Branch: "fix/3"}, BucketIssue, SourceBranch, 3},
		{"other org", AttributionInput{Org: org, Repo: &RepoRef{Owner: "someone", Name: "x"}, Branch: "feat/5-x"}, BucketExternal, SourceNone, 0},
		{"no repo", AttributionInput{Org: org}, BucketExternal, SourceNone, 0},
		{"override wins", AttributionInput{Org: org, Repo: repo("web"), Branch: "feat/5-x", Override: &ov}, BucketIssue, SourceManual, 9},
	}
	for _, c := range cases {
		a := Attribute(c.in)
		if err := a.Validate(); err != nil {
			t.Errorf("%s: invalid attribution: %v", c.name, err)
		}
		n := 0
		if a.Issue != nil {
			n = a.Issue.Number
		}
		if a.Bucket != c.bucket || a.Source != c.source || n != c.issue {
			t.Errorf("%s: got %+v", c.name, a)
		}
	}
	if a := Attribute(AttributionInput{Org: org, Repo: &RepoRef{Owner: "x", Name: "y"}, Branch: "secret"}); a.Branch != "" || a.Repo != nil {
		t.Errorf("external attribution leaks repository data: %+v", a)
	}
}

func TestReassign(t *testing.T) {
	a := Attribute(AttributionInput{Org: org, Repo: repo("api"), Branch: "main"})
	to := IssueRef{Repo: *repo("api"), Number: 4}
	b := a.Reassign(&to)
	if b.Bucket != BucketIssue || b.Source != SourceReassigned || b.Issue.Number != 4 || b.Validate() != nil {
		t.Fatalf("unexpected %+v", b)
	}
	if c := b.Reassign(nil); c.Bucket != BucketRepo || c.Validate() != nil {
		t.Fatalf("unexpected %+v", c)
	}
}

func TestPriceCostAndLookup(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 1, d, 0, 0, 0, 0, time.UTC) }
	pb := NewPriceBook([]Price{
		{ID: 1, Provider: "anthropic", Model: "claude-sonnet-5-5", Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75, EffectiveFrom: day(1), Source: PriceSourceModelsDev},
		{ID: 2, Provider: "anthropic", Model: "claude-sonnet-5-5", Input: 2, Output: 10, EffectiveFrom: day(10), Source: PriceSourceModelsDev},
		{ID: 3, Model: "glm-5", Input: 1, Output: 2, EffectiveFrom: day(1), Source: PriceSourceManual},
	})
	p, ok := pb.Lookup("anthropic", "anthropic/Claude-Sonnet-5-5", day(5))
	if !ok || p.ID != 1 {
		t.Fatalf("lookup day 5: %+v %v", p, ok)
	}
	if p, _ := pb.Lookup("anthropic", "claude-sonnet-5-5", day(20)); p.ID != 2 {
		t.Fatalf("lookup day 20 got %d", p.ID)
	}
	if p, ok := pb.Lookup("litellm", "glm-5", day(2)); !ok || p.ID != 3 {
		t.Fatalf("provider-agnostic manual price not found")
	}
	cost := p.Cost(Tokens{Input: 1_000_000, Output: 100_000, CacheRead: 1_000_000, CacheWrite: 10_000})
	// 3 + 1.5 + 0.3 + 0.0375 = 4.8375 USD
	if cost != 4_837_500 {
		t.Fatalf("cost = %d", cost)
	}
	ev := UsageEvent{Provider: "x", Model: "unknown", ReportedCost: 42, OccurredAt: day(2)}
	if c, _, priced := pb.Price(ev); priced || c != 42 {
		t.Fatalf("unknown model should fall back to reported cost")
	}
}

func TestTaskLifecycle(t *testing.T) {
	now := time.Now()
	task := Task{Ref: IssueRef{Repo: *repo("api"), Number: 1}, State: TaskTodo}
	steps := []Step{StepStart, StepStart, StepPR, StepMerge, StepPR}
	for _, s := range steps {
		if err := task.Apply(s, now, CostSnapshot{Cost: 100}); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if task.State != TaskDone || task.Snapshot == nil || task.Snapshot.Cost != 100 {
		t.Fatalf("unexpected task %+v", task)
	}
	if adj := task.Adjustment(130); adj != 30 {
		t.Fatalf("adjustment = %d", adj)
	}
	if err := task.Apply(StepStart, now, CostSnapshot{}); err == nil {
		t.Fatal("starting a closed task must fail")
	}
}

func TestDetectStatusMap(t *testing.T) {
	sm := DetectStatusMap("P", "F", []StatusOption{
		{ID: "a", Name: "📋 Backlog"}, {ID: "b", Name: "🏗 In progress"}, {ID: "c", Name: "👀 In Review"}, {ID: "d", Name: "✅ Done"},
	})
	if sm.Options[TaskInProgress] != "b" || sm.Options[TaskInReview] != "c" || sm.Options[TaskDone] != "d" || sm.Options[TaskTodo] != "a" {
		t.Fatalf("unexpected %+v", sm.Options)
	}
	if len(sm.Missing()) != 0 {
		t.Fatalf("missing %v", sm.Missing())
	}
}

func TestUsageValidate(t *testing.T) {
	e := UsageEvent{ExternalID: "r1", Harness: HarnessClaudeCode, Model: "m", OccurredAt: time.Now(),
		Attribution: Attribute(AttributionInput{Org: org})}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	if e.DedupKey() != "claude-code:r1" {
		t.Fatal(e.DedupKey())
	}
	e.Tokens.Input = -1
	if e.Validate() == nil {
		t.Fatal("negative tokens accepted")
	}
}
