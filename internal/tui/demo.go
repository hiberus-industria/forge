package tui

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/daemon/localapi"
	"github.com/hiberus-industria/forge/internal/devflow"
	"github.com/hiberus-industria/forge/internal/domain"
)

// demoBackend is an in-memory fake used with FORGE_TUI_DEMO=1 and in tests.
// Its data is generated deterministically from a seed and stays internally
// consistent: tasks, inbox and reports all aggregate the same events.
type demoBackend struct {
	mu       sync.Mutex
	now      func() time.Time
	rng      *rand.Rand
	org      string
	me       demoUser
	users    []demoUser
	tasks    []*demoTask
	events   []*demoEvent
	override *localapi.Override
	// liveEvery is the interval between generated live events (0 = none).
	liveEvery time.Duration
	live      []localapi.LiveEvent
	// stepDelay simulates the latency of each devflow step.
	stepDelay time.Duration
}

type demoUser struct {
	id    uuid.UUID
	name  string
	login string
}

type demoTask struct {
	ref    domain.IssueRef
	title  string
	labels []string
	state  api.TaskState
	who    []string
	start  *time.Time
	pr     *int
	closed *time.Time
	snap   *int64
	branch string
}

type demoEvent struct {
	id      uuid.UUID
	user    demoUser
	harness domain.Harness
	session string
	model   string
	tokens  domain.Tokens
	cost    int64
	at      time.Time
	bucket  domain.Bucket
	repo    string
	branch  string
	issue   *domain.IssueRef
	priced  bool
}

var demoModels = []struct {
	harness domain.Harness
	model   string
	in, out float64 // USD per 1M
	cacheR  float64
	priced  bool
	weight  int
}{
	{domain.HarnessClaudeCode, "claude-sonnet-5-5", 3, 15, 0.3, true, 6},
	{domain.HarnessClaudeCode, "claude-opus-5-5", 15, 75, 1.5, true, 2},
	{domain.HarnessClaudeCode, "claude-haiku-5-5", 1, 5, 0.1, true, 2},
	{domain.HarnessOpenCode, "glm-5", 0.6, 2.2, 0.1, true, 3},
	{domain.HarnessOpenCode, "qwen3-coder-plus", 0.8, 3, 0, false, 1},
}

func newDemoBackend(now time.Time) *demoBackend {
	b := &demoBackend{
		now:       time.Now,
		rng:       rand.New(rand.NewPCG(42, 7)),
		org:       "hiberus-industria",
		liveEvery: 2 * time.Second,
		stepDelay: 450 * time.Millisecond,
	}
	b.users = []demoUser{
		{uuid.MustParse("00000000-0000-0000-0000-000000000001"), "Ana Ruiz", "aruiz"},
		{uuid.MustParse("00000000-0000-0000-0000-000000000002"), "Luis Martín", "lmartin"},
		{uuid.MustParse("00000000-0000-0000-0000-000000000003"), "Marta Gil", "mgil"},
	}
	b.me = b.users[0]
	b.seed(now)
	return b
}

func (b *demoBackend) seed(now time.Time) {
	type spec struct {
		repo, title string
		labels      []string
		state       api.TaskState
		who         int
	}
	specs := []spec{
		{"api", "Login OIDC con Keycloak (PKCE + device flow)", []string{"enhancement"}, api.InProgress, 0},
		{"api", "Cálculo de coste con caché de 1h", []string{"bug"}, api.InReview, 0},
		{"api", "Reconciliación periódica con GitHub", []string{"enhancement"}, api.Todo, 0},
		{"api", "Endpoint de reasignación en bloque", nil, api.Done, 1},
		{"web", "Panel de costes por cliente en la SPA", []string{"enhancement"}, api.InProgress, 1},
		{"web", "Corregir gráfico de barras en modo claro", []string{"bug"}, api.Todo, 0},
		{"web", "Exportación CSV de informes", nil, api.Done, 2},
		{"infra", "Caddy con TLS para webhooks de GitHub", []string{"chore"}, api.InProgress, 2},
		{"infra", "Backups diarios de Postgres", []string{"chore"}, api.Todo, 1},
		{"infra", "Documentar realm de Keycloak", []string{"documentation"}, api.Done, 0},
	}
	for i, sp := range specs {
		ref := domain.IssueRef{Repo: domain.RepoRef{Owner: b.org, Name: sp.repo}, Number: 100 + i*7}
		t := &demoTask{ref: ref, title: sp.title, labels: sp.labels, state: sp.state, who: []string{b.users[sp.who].login}}
		if sp.state != api.Todo {
			st := now.Add(-time.Duration(3+i) * 24 * time.Hour)
			t.start = &st
			t.branch = domain.BranchName(domain.BranchTypeFromLabels(sp.labels), ref.Number, sp.title)
		}
		if sp.state == api.InReview || sp.state == api.Done {
			n := 200 + i
			t.pr = &n
		}
		b.tasks = append(b.tasks, t)
	}
	// 30 days of events, attributed to started tasks or left unassigned.
	for d := 29; d >= 0; d-- {
		day := now.Add(-time.Duration(d) * 24 * time.Hour)
		for _, u := range b.users {
			sessions := b.rng.IntN(3)
			for s := 0; s < sessions; s++ {
				session := fmt.Sprintf("ses_%s_%d_%d", u.login, d, s)
				var task *demoTask
				var repo, branch string
				bucket := domain.BucketIssue
				switch r := b.rng.IntN(10); {
				case r < 7:
					task = b.tasks[b.rng.IntN(len(b.tasks))]
					if task.start == nil || task.start.After(day) {
						task = nil
						repo, branch, bucket = b.org+"/"+[]string{"api", "web", "infra"}[b.rng.IntN(3)], "main", domain.BucketRepo
					} else {
						repo, branch = task.ref.Repo.String(), task.branch
					}
				case r < 9:
					repo, branch, bucket = b.org+"/"+[]string{"api", "web", "infra"}[b.rng.IntN(3)], "main", domain.BucketRepo
				default:
					bucket = domain.BucketExternal
				}
				at := time.Date(day.Year(), day.Month(), day.Day(), 9+b.rng.IntN(9), b.rng.IntN(60), 0, 0, now.Location())
				mi := b.pickModel()
				for k := 0; k < 4+b.rng.IntN(20); k++ {
					ev := b.genEvent(u, session, at.Add(time.Duration(k)*90*time.Second), bucket, repo, branch, task, mi)
					if ev.at.After(now) {
						continue
					}
					b.events = append(b.events, ev)
				}
			}
		}
	}
	for _, t := range b.tasks {
		if t.state == api.Done {
			c := b.taskCost(t.ref)
			closed := now.Add(-36 * time.Hour)
			t.closed = &closed
			snap := c * 9 / 10 // some spend arrived after closing
			t.snap = &snap
		}
	}
}

func (b *demoBackend) pickModel() int {
	total := 0
	for _, m := range demoModels {
		total += m.weight
	}
	r := b.rng.IntN(total)
	for i, m := range demoModels {
		if r < m.weight {
			return i
		}
		r -= m.weight
	}
	return 0
}

// genEvent fakes a request; sessions stick to one model, with the
// occasional cheap side call (Claude Code uses Haiku for small tasks).
func (b *demoBackend) genEvent(u demoUser, session string, at time.Time, bucket domain.Bucket, repo, branch string, task *demoTask, mi int) *demoEvent {
	m := demoModels[mi]
	if m.harness == domain.HarnessClaudeCode && b.rng.IntN(6) == 0 {
		m = demoModels[2]
	}
	tk := domain.Tokens{
		Input:     int64(500 + b.rng.IntN(6000)),
		Output:    int64(200 + b.rng.IntN(3000)),
		CacheRead: int64(b.rng.IntN(80000)),
	}
	if m.harness == domain.HarnessOpenCode {
		tk.Reasoning = int64(b.rng.IntN(1500))
	}
	cost := (float64(tk.Input)*m.in + float64(tk.Output+tk.Reasoning)*m.out + float64(tk.CacheRead)*m.cacheR) // micros
	ev := &demoEvent{
		id: uuid.New(), user: u, harness: m.harness, session: session, model: m.model, tokens: tk,
		cost: int64(cost), at: at, bucket: bucket, repo: repo, branch: branch, priced: m.priced,
	}
	if task != nil {
		r := task.ref
		ev.issue = &r
	}
	return ev
}

func (b *demoBackend) taskCost(ref domain.IssueRef) int64 {
	var c int64
	for _, e := range b.events {
		if e.issue != nil && *e.issue == ref {
			c += e.cost
		}
	}
	return c
}

func (b *demoBackend) summary(u demoUser) api.UserSummary {
	login := u.login
	return api.UserSummary{Id: u.id, Name: u.name, GithubLogin: &login}
}

func toAPITokens(t domain.Tokens) api.Tokens { return apiTokens(t) }

// ---------------------------------------------------------------- daemon side

func (b *demoBackend) Status(context.Context) (localapi.Status, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	last := now.Add(-3 * time.Second)
	return localapi.Status{
		Version: "demo", MachineID: "demo-machine", StartedAt: now.Add(-3 * time.Hour),
		APIURL: "https://forge.demo", Org: b.org, IngestURL: "http://127.0.0.1:47318",
		LoggedIn: true, User: &localapi.UserInfo{Name: b.me.name, Email: "ana@example.com", GitHubLogin: b.me.login},
		Pending: len(b.live) % 3, LastSyncAt: &last,
		Captures: []localapi.CaptureStatus{
			{Harness: domain.HarnessClaudeCode, LastEventAt: &last, EventsToday: 42},
			{Harness: domain.HarnessOpenCode, EventsToday: 9},
		},
		Override: b.override,
	}, nil
}

func (b *demoBackend) liveEvent(at time.Time) localapi.LiveEvent {
	ref := b.tasks[0].ref
	attr := domain.Attribution{Bucket: domain.BucketIssue, Source: domain.SourceBranch, Repo: &ref.Repo, Branch: b.tasks[0].branch, Issue: &ref}
	if b.override != nil {
		ov := b.override.Issue
		attr = domain.Attribution{Bucket: domain.BucketIssue, Source: domain.SourceManual, Repo: &ov.Repo, Issue: &ov}
	}
	var task *demoTask
	for _, t := range b.tasks {
		if attr.Issue != nil && t.ref == *attr.Issue {
			task = t
		}
	}
	ev := b.genEvent(b.me, "live-session", at, domain.BucketIssue, attr.Repo.String(), attr.Branch, task, 0)
	b.events = append(b.events, ev)
	return localapi.LiveEvent{
		Harness: ev.harness, SessionID: ev.session, Provider: "anthropic", Model: ev.model, Tokens: ev.tokens,
		CostMicros: domain.Micros(ev.cost), OccurredAt: at, Attribution: attr, Synced: b.rng.IntN(4) != 0,
	}
}

func (b *demoBackend) Recent(_ context.Context, limit int) ([]localapi.LiveEvent, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.live) == 0 {
		now := b.now()
		for i := 12; i > 0; i-- {
			b.live = append([]localapi.LiveEvent{b.liveEvent(now.Add(-time.Duration(i) * 75 * time.Second))}, b.live...)
		}
	}
	return append([]localapi.LiveEvent(nil), b.live[:min(limit, len(b.live))]...), nil
}

func (b *demoBackend) Live(ctx context.Context) (<-chan localapi.LiveEvent, error) {
	ch := make(chan localapi.LiveEvent)
	if b.liveEvery <= 0 {
		return ch, nil
	}
	go func() {
		defer close(ch)
		t := time.NewTicker(b.liveEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				b.mu.Lock()
				ev := b.liveEvent(b.now())
				b.live = append([]localapi.LiveEvent{ev}, b.live...)
				b.mu.Unlock()
				select {
				case ch <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch, nil
}

func (b *demoBackend) SetOverride(_ context.Context, r localapi.OverrideRequest) (localapi.Override, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	o := localapi.Override{Issue: r.Issue, Scope: r.Scope, RepoPath: r.RepoPath, SetAt: b.now()}
	b.override = &o
	return o, nil
}

func (b *demoBackend) ClearOverride(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.override = nil
	return nil
}

// ---------------------------------------------------------------- API side

func (b *demoBackend) apiTask(t *demoTask) api.Task {
	var cost int64
	var tk domain.Tokens
	n := 0
	var last *time.Time
	for _, e := range b.events {
		if e.issue != nil && *e.issue == t.ref {
			cost += e.cost
			tk = tk.Add(e.tokens)
			n++
			if last == nil || e.at.After(*last) {
				at := e.at
				last = &at
			}
		}
	}
	at := api.Task{
		Ref: api.IssueRef{Repo: t.ref.Repo.String(), Number: t.ref.Number}, Title: t.title,
		Url: fmt.Sprintf("https://github.com/%s/issues/%d", t.ref.Repo, t.ref.Number), State: t.state,
		Assignees: t.who, Labels: append([]string{}, t.labels...), StartedAt: t.start, PrNumber: t.pr,
		ClosedAt: t.closed, CostUsdMicros: cost, Tokens: toAPITokens(tk), Events: n, LastActivityAt: last,
	}
	if t.branch != "" {
		br := t.branch
		at.Branch = &br
	}
	if t.snap != nil {
		at.SnapshotUsdMicros = t.snap
		adj := cost - *t.snap
		at.AdjustmentUsdMicros = &adj
	}
	return at
}

func (b *demoBackend) Tasks(_ context.Context, p api.ListTasksParams) ([]api.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []api.Task
	for _, t := range b.tasks {
		if p.Assignee == nil || *p.Assignee == "me" {
			if !contains(t.who, b.me.login) {
				continue
			}
		}
		if p.State != nil && len(*p.State) > 0 && !containsState(*p.State, t.state) {
			continue
		}
		out = append(out, b.apiTask(t))
	}
	return out, nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func containsState(xs []api.TaskState, x api.TaskState) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func (b *demoBackend) Task(_ context.Context, ref domain.IssueRef) (api.TaskDetail, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, t := range b.tasks {
		if t.ref != ref {
			continue
		}
		var evs []*demoEvent
		for _, e := range b.events {
			if e.issue != nil && *e.issue == ref {
				evs = append(evs, e)
			}
		}
		return api.TaskDetail{
			Task:    b.apiTask(t),
			ByModel: aggregate(evs, func(e *demoEvent) (string, string) { return e.model, e.model }, 0),
			ByUser:  aggregate(evs, func(e *demoEvent) (string, string) { return e.user.id.String(), "@" + e.user.login }, 0),
			ByDay:   sortByKey(aggregate(evs, func(e *demoEvent) (string, string) { d := e.at.Format("2006-01-02"); return d, d }, 0)),
		}, nil
	}
	return api.TaskDetail{}, fmt.Errorf("API 404: %s no existe", ref)
}

func aggregate(evs []*demoEvent, key func(*demoEvent) (string, string), usdPerEUR float64) []api.CostRow {
	idx := map[string]int{}
	var rows []api.CostRow
	for _, e := range evs {
		k, label := key(e)
		i, ok := idx[k]
		if !ok {
			i = len(rows)
			idx[k] = i
			rows = append(rows, api.CostRow{Key: k, Label: label})
		}
		r := &rows[i]
		r.CostUsdMicros += e.cost
		r.Events++
		r.Tokens = addTokens(r.Tokens, toAPITokens(e.tokens))
	}
	for i := range rows {
		usd := domain.Micros(rows[i].CostUsdMicros).USD()
		if usdPerEUR > 0 {
			rows[i].Cost = usd / usdPerEUR
		} else {
			rows[i].Cost = usd
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].CostUsdMicros > rows[j].CostUsdMicros })
	return rows
}

func sortByKey(rows []api.CostRow) []api.CostRow {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
	return rows
}

func (b *demoBackend) Inbox(_ context.Context, p api.ListInboxParams) ([]api.InboxGroup, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	idx := map[string]int{}
	var out []api.InboxGroup
	for _, e := range b.events {
		if e.bucket == domain.BucketIssue {
			continue
		}
		if p.User != nil && *p.User == "me" && e.user.id != b.me.id {
			continue
		}
		k := e.session + e.repo + e.branch
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			g := api.InboxGroup{
				Bucket: api.Bucket(e.bucket), Harness: api.Harness(e.harness), SessionId: e.session,
				User: b.summary(e.user), FirstAt: e.at, LastAt: e.at, Models: &[]string{},
			}
			if e.repo != "" {
				r, br := e.repo, e.branch
				g.Repo, g.Branch = &r, &br
			}
			out = append(out, g)
		}
		g := &out[i]
		g.Events++
		g.CostUsdMicros += e.cost
		g.EventIds = append(g.EventIds, e.id)
		if e.at.After(g.LastAt) {
			g.LastAt = e.at
		}
		if !contains(*g.Models, e.model) {
			*g.Models = append(*g.Models, e.model)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastAt.After(out[j].LastAt) })
	return out, nil
}

func (b *demoBackend) Reassign(_ context.Context, r api.ReassignRequest) (api.ReassignResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ids := map[uuid.UUID]bool{}
	for _, id := range r.EventIds {
		ids[id] = true
	}
	var res api.ReassignResult
	for _, e := range b.events {
		if !ids[e.id] {
			continue
		}
		if e.user.id != b.me.id {
			res.Skipped++
			continue
		}
		res.Updated++
		if r.To == nil {
			e.issue = nil
			if e.repo != "" {
				e.bucket = domain.BucketRepo
			} else {
				e.bucket = domain.BucketExternal
			}
			continue
		}
		ref, err := domain.ParseIssueRef(fmt.Sprintf("%s#%d", r.To.Repo, r.To.Number))
		if err != nil {
			return api.ReassignResult{}, err
		}
		e.issue, e.bucket, e.repo = &ref, domain.BucketIssue, ref.Repo.String()
	}
	return res, nil
}

func (b *demoBackend) UnassignTask(ctx context.Context, ref domain.IssueRef) (api.ReassignResult, error) {
	b.mu.Lock()
	var req api.ReassignRequest
	for _, e := range b.events {
		if e.issue != nil && *e.issue == ref && e.user.id == b.me.id {
			req.EventIds = append(req.EventIds, e.id)
		}
	}
	b.mu.Unlock()
	return b.Reassign(ctx, req)
}

// demoUSDPerEUR is the fake ECB rate.
const demoUSDPerEUR = 1.08

func (b *demoBackend) Report(_ context.Context, p api.GetCostReportParams) (api.CostReport, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var evs []*demoEvent
	for _, e := range b.events {
		if p.From != nil && e.at.Before(*p.From) || p.To != nil && !e.at.Before(*p.To) {
			continue
		}
		evs = append(evs, e)
	}
	rate := 0.0
	cur := api.USD
	if p.Currency != nil && *p.Currency == api.EUR {
		rate, cur = demoUSDPerEUR, api.EUR
	}
	var key func(*demoEvent) (string, string)
	switch p.GroupBy {
	case api.GroupByRepo:
		key = func(e *demoEvent) (string, string) {
			if e.repo == "" {
				return "-", "personal / otros"
			}
			return e.repo, e.repo
		}
	case api.GroupByIssue:
		key = func(e *demoEvent) (string, string) {
			if e.issue == nil {
				return "-", "sin asignar"
			}
			return e.issue.String(), e.issue.String()
		}
	case api.GroupByUser:
		key = func(e *demoEvent) (string, string) { return e.user.id.String(), e.user.name }
	case api.GroupByModel:
		key = func(e *demoEvent) (string, string) { return e.model, e.model }
	case api.GroupByHarness:
		key = func(e *demoEvent) (string, string) { return string(e.harness), harnessName(string(e.harness)) }
	default:
		key = func(e *demoEvent) (string, string) { d := e.at.Format("2006-01-02"); return d, d }
	}
	rows := aggregate(evs, key, rate)
	if p.GroupBy == api.GroupByDay {
		rows = sortByKey(rows)
	}
	r := api.CostReport{Currency: cur, GroupBy: p.GroupBy, From: p.From, To: p.To, Rows: rows}
	unpriced := 0
	for _, e := range evs {
		r.TotalUsdMicros += e.cost
		if !e.priced {
			unpriced++
		}
	}
	r.UnpricedEvents = &unpriced
	r.Total = domain.Micros(r.TotalUsdMicros).USD()
	if rate > 0 {
		r.Total /= rate
	}
	return r, nil
}

func (b *demoBackend) Start(ctx context.Context, o devflow.StartOptions) (devflow.StartResult, error) {
	b.mu.Lock()
	var task *demoTask
	for _, t := range b.tasks {
		if t.ref == o.Issue {
			task = t
		}
	}
	b.mu.Unlock()
	if task == nil {
		return devflow.StartResult{}, fmt.Errorf("%s no existe", o.Issue)
	}
	br := domain.BranchName(o.Type, o.Issue.Number, task.title)
	steps := []string{
		"Leyendo el issue " + o.Issue.String(),
		"Actualizando la rama por defecto (git fetch)",
		"Creando la rama " + br,
		"Asignándote el issue en GitHub",
		"Moviendo el Project a In Progress",
	}
	if err := demoSteps(ctx, o.Progress, steps, b.stepDelay); err != nil {
		return devflow.StartResult{}, err
	}
	b.mu.Lock()
	now := b.now()
	task.state, task.branch, task.start = api.InProgress, br, &now
	if !contains(task.who, b.me.login) {
		task.who = append(task.who, b.me.login)
	}
	b.mu.Unlock()
	return devflow.StartResult{Branch: br, Created: true, Title: task.title}, nil
}

func (b *demoBackend) OpenPR(ctx context.Context, o devflow.PROptions) (devflow.PRResult, error) {
	steps := []string{"Empujando la rama a origin", "Creando la pull request (Closes #" + fmt.Sprint(o.Issue.Number) + ")", "Moviendo el Project a In Review"}
	if err := demoSteps(ctx, o.Progress, steps, b.stepDelay); err != nil {
		return devflow.PRResult{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 300 + b.rng.IntN(100)
	for _, t := range b.tasks {
		if o.Issue != nil && t.ref == *o.Issue {
			t.state, t.pr = api.InReview, &n
		}
	}
	return devflow.PRResult{Number: n, URL: fmt.Sprintf("https://github.com/%s/pull/%d", o.Issue.Repo, n)}, nil
}

func demoSteps(ctx context.Context, progress func(string), steps []string, delay time.Duration) error {
	for _, s := range steps {
		if progress != nil {
			progress(s)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return nil
}
