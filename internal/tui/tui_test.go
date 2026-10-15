package tui

import (
	"context"
	"encoding/csv"
	"image/color"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/domain"
	"github.com/hiberus-industria/forge/internal/tui/theme"
)

var fixedNow = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

// newTestModel builds the root model over the demo backend with a frozen clock.
func newTestModel(t *testing.T) (*model, *demoBackend) {
	t.Helper()
	time.Local = time.UTC
	be := newDemoBackend(fixedNow)
	be.now = func() time.Time { return fixedNow }
	be.liveEvery = 0
	be.stepDelay = 5 * time.Millisecond
	sh := newShared(context.Background(), be, theme.Default(), "hiberus-industria", domain.CurrencyEUR, t.TempDir())
	sh.now = func() time.Time { return fixedNow }
	m := newModel(sh, "test")
	return m, be
}

// within runs cmd, giving up after d (ticks and blocking subscriptions).
func within(cmd tea.Cmd, d time.Duration) tea.Msg {
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(d):
		return nil
	}
}

// drive executes cmd and feeds resulting messages back into the model.
func drive(m tea.Model, cmd tea.Cmd, depth int) tea.Model {
	if cmd == nil || depth > 8 {
		return m
	}
	msg := within(cmd, 250*time.Millisecond)
	if msg == nil {
		return m
	}
	// Batch and Sequence messages are slices of commands; run them in order.
	if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
		for i := range v.Len() {
			m = drive(m, v.Index(i).Interface().(tea.Cmd), depth+1)
		}
		return m
	}
	switch msg := msg.(type) {
	default:
		next, c := m.Update(msg)
		return drive(next, c, depth+1)
	}
}

func boot(t *testing.T) *model {
	m, _ := newTestModel(t)
	var tm tea.Model = m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	tm = drive(tm, m.Init(), 0)
	return tm.(*model)
}

func keyMsg(s string) tea.KeyPressMsg {
	switch s {
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func press(m *model, keys ...string) *model {
	var tm tea.Model = m
	for _, k := range keys {
		var cmd tea.Cmd
		tm, cmd = tm.Update(keyMsg(k))
		tm = drive(tm, cmd, 0)
	}
	return tm.(*model)
}

func TestGoldenScreens(t *testing.T) {
	m := boot(t)
	if m.splash {
		t.Fatal("splash should end once the status is loaded")
	}
	for i, name := range []string{"tasks", "live", "inbox", "reports"} {
		m = press(m, string(rune('1'+i)))
		if m.active != i {
			t.Fatalf("tab %d not selected", i)
		}
		out := ansi.Strip(m.render())
		if got := len(strings.Split(out, "\n")); got != 40 {
			t.Errorf("%s: rendered %d lines, want 40", name, got)
		}
		for _, line := range strings.Split(out, "\n") {
			if w := ansi.StringWidth(line); w > 120 {
				t.Errorf("%s: line wider than terminal (%d): %q", name, w, line)
			}
		}
		golden(t, name, out)
	}
	m = press(m, "1", "s")
	golden(t, "start_modal", ansi.Strip(m.render()))
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s (run with UPDATE_GOLDEN=1): %v", path, err)
	}
	if string(want) != got {
		t.Errorf("%s differs from golden; run UPDATE_GOLDEN=1 go test ./internal/tui/ and review the diff", path)
	}
}

func TestTabKeysAndCapture(t *testing.T) {
	m := boot(t)
	m = press(m, "/") // start filtering on Tareas
	if !m.screens[0].Capturing() {
		t.Fatal("filter should capture keys")
	}
	m = press(m, "2", "q") // typed into the filter, not tab switch / quit
	if m.active != 0 {
		t.Fatal("digits must go to the filter while capturing")
	}
	if v := m.screens[0].(*tasksScreen).filter.Value(); v != "2q" {
		t.Fatalf("filter value = %q", v)
	}
	m = press(m, "esc", "3")
	if m.active != 2 {
		t.Fatalf("active = %d, want inbox", m.active)
	}
}

func TestInboxSelectionAndReassign(t *testing.T) {
	m := boot(t)
	m = press(m, "3", "space", "space")
	in := m.screens[2].(*inboxScreen)
	if len(in.selected) != 2 {
		t.Fatalf("selected = %d, want 2", len(in.selected))
	}
	groups := in.selection()
	ref := domain.IssueRef{Repo: domain.RepoRef{Owner: "hiberus-industria", Name: "api"}, Number: 100}
	req, cost := buildReassign(append(groups, groups[0]), &ref, "triaje")
	want := len(groups[0].EventIds) + len(groups[1].EventIds)
	if len(req.EventIds) != want {
		t.Fatalf("event ids = %d, want %d (deduplicated)", len(req.EventIds), want)
	}
	if req.To == nil || req.To.Repo != "hiberus-industria/api" || req.To.Number != 100 || *req.Reason != "triaje" {
		t.Fatalf("bad request %+v", req)
	}
	if cost != groups[0].CostUsdMicros*2+groups[1].CostUsdMicros {
		t.Fatalf("cost = %d", cost)
	}

	// Full flow: enter opens the picker, typed ref + confirm reassigns.
	before := len(in.groups)
	m = press(m, "enter")
	if _, ok := m.modal.(*pickerModal); !ok {
		t.Fatalf("expected picker modal, got %T", m.modal)
	}
	for _, r := range "api#100" {
		m = press(m, string(r))
	}
	m = press(m, "enter")
	if _, ok := m.modal.(*confirmModal); !ok {
		t.Fatalf("expected confirm modal, got %T", m.modal)
	}
	m = press(m, "enter")
	in = m.screens[2].(*inboxScreen)
	if len(in.groups) >= before {
		t.Fatalf("inbox should shrink after reassigning: before %d after %d", before, len(in.groups))
	}
	if len(m.toasts) == 0 || !strings.Contains(m.toasts[len(m.toasts)-1].text, "hiberus-industria/api#100") {
		t.Fatalf("expected success toast, got %+v", m.toasts)
	}
}

func TestReportCSVExport(t *testing.T) {
	dir := t.TempDir()
	in := int64(1000)
	r := api.CostReport{
		Currency: api.EUR, GroupBy: api.GroupByRepo, Total: 10,
		Rows: []api.CostRow{{Key: "o/a", Label: "o/a, \"quoted\"", Cost: 9.5, CostUsdMicros: 10_260_000, Events: 3, Tokens: api.Tokens{Input: in}}},
	}
	p := period{from: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), to: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	path, err := writeReportCSV(dir, r, p)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "forge-repo-20260901_20260930.csv" {
		t.Fatalf("name = %s", filepath.Base(path))
	}
	f, _ := os.Open(path)
	defer f.Close()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[1][1] != `o/a, "quoted"` || recs[1][3] != "EUR" || recs[1][4] != "10.260000" || recs[1][6] != "1000" {
		t.Fatalf("unexpected csv %v", recs)
	}
}

func TestExportFromScreen(t *testing.T) {
	m := boot(t)
	m = press(m, "4", "e")
	entries, _ := os.ReadDir(m.sh.exportDir)
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".csv") {
		t.Fatalf("expected one csv in export dir, got %v", entries)
	}
}

func TestParsers(t *testing.T) {
	if r, err := parseLooseRef("api#12", "hiberus-industria"); err != nil || r.String() != "hiberus-industria/api#12" {
		t.Fatalf("short ref: %v %v", r, err)
	}
	if _, err := parseLooseRef("#12", "x"); err == nil {
		t.Fatal("bare number must be rejected")
	}
	p, err := parseCustomPeriod("2026-09-01..2026-09-30", time.UTC)
	if err != nil || !p.to.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("period %+v %v", p, err)
	}
	if _, err := parseCustomPeriod("2026-09-30..2026-09-01", time.UTC); err == nil {
		t.Fatal("reversed period accepted")
	}
	if fuzzyScore("okc", "Login OIDC con Keycloak") < 0 || fuzzyScore("zzz", "Login") >= 0 {
		t.Fatal("fuzzy matching broken")
	}
}

func TestTasksGroupingAndFilter(t *testing.T) {
	m := boot(t)
	ts := m.screens[0].(*tasksScreen)
	vis := ts.visible()
	if len(vis) == 0 {
		t.Fatal("demo should have tasks for the current user")
	}
	for i := 1; i < len(vis); i++ {
		if vis[i-1].Ref.Repo > vis[i].Ref.Repo {
			t.Fatal("tasks must be grouped by repository")
		}
	}
	ts.filter.SetValue("keycloak")
	if v := ts.visible(); len(v) != 1 {
		t.Fatalf("filter keycloak = %d tasks", len(v))
	}
}

func TestStartFlowFromTasks(t *testing.T) {
	m := boot(t)
	m = press(m, "s")
	if _, ok := m.modal.(*choiceModal); !ok {
		t.Fatalf("expected branch type chooser, got %T", m.modal)
	}
	m = press(m, "enter")
	fm, ok := m.modal.(*flowModal)
	if !ok {
		t.Fatalf("expected flow modal, got %T", m.modal)
	}
	// Drain the flow until it ends.
	var tm tea.Model = m
	deadline := time.Now().Add(5 * time.Second)
	for !fm.done && time.Now().Before(deadline) {
		tm = drive(tm, waitFlow(fm.ch), 0)
	}
	if !fm.done || fm.err != nil || len(fm.steps) < 3 {
		t.Fatalf("flow not completed: done=%v err=%v steps=%v", fm.done, fm.err, fm.steps)
	}
}

func TestNarrowLightWithModalAndToast(t *testing.T) {
	m := boot(t)
	var tm tea.Model = m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	tm, _ = tm.Update(tea.BackgroundColorMsg{Color: color.White})
	m = tm.(*model)
	if m.sh.st.IsDark {
		t.Fatal("light background should switch to light styles")
	}
	for i := range 4 {
		m = press(m, string(rune('1'+i)))
		tm, _ = m.Update(toastMsg{text: "Hola desde un toast bastante largo para comprobar el ajuste", kind: toastOK})
		m = tm.(*model)
		out := ansi.Strip(m.render())
		lines := strings.Split(out, "\n")
		if len(lines) != 24 {
			t.Fatalf("tab %d: %d lines", i, len(lines))
		}
		for _, l := range lines {
			if ansi.StringWidth(l) > 80 {
				t.Fatalf("tab %d: line too wide: %q", i, l)
			}
		}
	}
	m = press(m, "1", "enter") // narrow: full-screen detail
	m = press(m, "s")
	if _, ok := m.modal.(*choiceModal); !ok {
		t.Fatalf("expected chooser, got %T", m.modal)
	}
	if out := ansi.Strip(m.render()); !strings.Contains(out, "Empezar hiberus-industria/api#100") {
		t.Fatalf("modal not composed over the screen:\n%s", out)
	}
}
