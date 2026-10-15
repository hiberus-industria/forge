package tui

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/daemon/localapi"
	"github.com/hiberus-industria/forge/internal/devflow"
	"github.com/hiberus-industria/forge/internal/domain"
)

type tasksKeys struct {
	Up, Down, Detail, Filter, Mine, States, Refresh, Start, PR, Override, Unassign key.Binding
}

func (k tasksKeys) ShortHelp() []key.Binding {
	return []key.Binding{k.Start, k.PR, k.Override, k.Detail, k.Filter, k.Mine}
}

func (k tasksKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Detail, k.Filter},
		{k.Mine, k.States, k.Refresh},
		{k.Start, k.PR, k.Override, k.Unassign},
	}
}

var taskKeyMap = tasksKeys{
	Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "subir")),
	Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "bajar")),
	Detail:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "detalle")),
	Filter:   key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filtrar")),
	Mine:     key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "mías/todas")),
	States:   key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "estado")),
	Refresh:  key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "recargar")),
	Start:    key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "empezar")),
	PR:       key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "abrir PR")),
	Override: key.NewBinding(key.WithKeys("o", "O"), key.WithHelp("o/O", "fijar/quitar tarea activa")),
	Unassign: key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "devolver a bandeja")),
}

// stateFilters are the presets cycled with `f`.
var stateFilters = []struct {
	label  string
	states []api.TaskState
}{
	{"activas", []api.TaskState{api.Todo, api.InProgress, api.InReview}},
	{"en curso", []api.TaskState{api.InProgress, api.InReview}},
	{"cerradas", []api.TaskState{api.Done}},
	{"todas", nil},
}

type tasksLoadedMsg struct {
	seq   int
	tasks []api.Task
	err   error
}

type taskDetailMsg struct {
	key    string
	detail api.TaskDetail
	err    error
}

type detailDebounceMsg struct{ seq int }

type tasksScreen struct {
	sh *shared

	tasks   []api.Task
	loading bool
	err     error
	seq     int

	all         bool
	stateFilter int
	filter      textinput.Model
	filtering   bool

	cursor     int
	detail     *api.TaskDetail
	detailKey  string
	detailSeq  int
	detailErr  error
	showDetail bool // narrow layouts: full-screen detail toggle
}

func newTasksScreen(sh *shared) *tasksScreen {
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "título, repo, #número o etiqueta"
	ti.SetStyles(inputStyles(sh.st))
	ti.SetWidth(40)
	return &tasksScreen{sh: sh, filter: ti, loading: true}
}

func (m *tasksScreen) Keys() help.KeyMap { return taskKeyMap }
func (m *tasksScreen) Capturing() bool   { return m.filtering }

func (m *tasksScreen) Init() tea.Cmd { return m.load() }

func (m *tasksScreen) load() tea.Cmd {
	m.seq++
	seq, all, sf := m.seq, m.all, stateFilters[m.stateFilter].states
	be, ctx := m.sh.be, m.sh.ctx
	m.loading = true
	return func() tea.Msg {
		assignee := "me"
		if all {
			assignee = "all"
		}
		p := api.ListTasksParams{Assignee: &assignee}
		if sf != nil {
			s := append([]api.TaskState(nil), sf...)
			p.State = &s
		}
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		ts, err := be.Tasks(ctx, p)
		return tasksLoadedMsg{seq: seq, tasks: ts, err: err}
	}
}

var stateOrder = map[api.TaskState]int{api.InProgress: 0, api.InReview: 1, api.Todo: 2, api.Done: 3}

// visible returns the filtered tasks sorted by repo, state and recency.
func (m *tasksScreen) visible() []api.Task {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	out := make([]api.Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		if q != "" {
			hay := strings.ToLower(fmt.Sprintf("%s#%d %s %s", t.Ref.Repo, t.Ref.Number, t.Title, strings.Join(t.Labels, " ")))
			if !strings.Contains(hay, strings.TrimPrefix(q, "#")) {
				continue
			}
		}
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Ref.Repo != b.Ref.Repo {
			return a.Ref.Repo < b.Ref.Repo
		}
		if stateOrder[a.State] != stateOrder[b.State] {
			return stateOrder[a.State] < stateOrder[b.State]
		}
		return a.Ref.Number > b.Ref.Number
	})
	return out
}

func (m *tasksScreen) selected() (api.Task, bool) {
	v := m.visible()
	if m.cursor < 0 || m.cursor >= len(v) {
		return api.Task{}, false
	}
	return v[m.cursor], true
}

func taskKey(t api.Task) string { return fmt.Sprintf("%s#%d", t.Ref.Repo, t.Ref.Number) }

// scheduleDetail debounces detail fetching while the cursor moves.
func (m *tasksScreen) scheduleDetail() tea.Cmd {
	t, ok := m.selected()
	if !ok || taskKey(t) == m.detailKey {
		return nil
	}
	m.detailSeq++
	seq := m.detailSeq
	return tea.Tick(180*time.Millisecond, func(time.Time) tea.Msg { return detailDebounceMsg{seq} })
}

func (m *tasksScreen) fetchDetail(t api.Task) tea.Cmd {
	k := taskKey(t)
	be, ctx := m.sh.be, m.sh.ctx
	return func() tea.Msg {
		ref, err := issueRef(t.Ref)
		if err != nil {
			return taskDetailMsg{key: k, err: err}
		}
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		d, err := be.Task(ctx, ref)
		return taskDetailMsg{key: k, detail: d, err: err}
	}
}

func (m *tasksScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tasksLoadedMsg:
		if msg.seq != m.seq {
			return m, nil
		}
		m.loading = false
		m.err = msg.err
		if msg.err == nil {
			m.tasks = msg.tasks
			m.cursor = min(m.cursor, max(0, len(m.visible())-1))
			m.detailKey = ""
			return m, m.scheduleDetail()
		}
		return m, nil
	case detailDebounceMsg:
		if msg.seq == m.detailSeq {
			if t, ok := m.selected(); ok {
				return m, m.fetchDetail(t)
			}
		}
	case taskDetailMsg:
		if t, ok := m.selected(); ok && taskKey(t) == msg.key {
			m.detailKey = msg.key
			m.detailErr = msg.err
			if msg.err == nil {
				d := msg.detail
				m.detail = &d
			}
		}
	case refreshMsg:
		m.detailKey = ""
		return m, m.load()
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			return m, m.move(-1)
		case tea.MouseWheelDown:
			return m, m.move(1)
		}
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *tasksScreen) move(d int) tea.Cmd {
	n := len(m.visible())
	if n == 0 {
		return nil
	}
	m.cursor = max(0, min(n-1, m.cursor+d))
	return m.scheduleDetail()
}

func (m *tasksScreen) handleKey(k tea.KeyPressMsg) (screen, tea.Cmd) {
	if m.filtering {
		switch k.String() {
		case "esc":
			m.filtering = false
			m.filter.SetValue("")
			m.filter.Blur()
			m.cursor = 0
			return m, m.scheduleDetail()
		case "enter":
			m.filtering = false
			m.filter.Blur()
			return m, m.scheduleDetail()
		case "up", "down":
			// allow navigating while filtering
		default:
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(k)
			m.cursor = 0
			return m, tea.Batch(cmd, m.scheduleDetail())
		}
	}
	switch {
	case key.Matches(k, taskKeyMap.Up):
		return m, m.move(-1)
	case key.Matches(k, taskKeyMap.Down):
		return m, m.move(1)
	case k.String() == "g" || k.String() == "home":
		m.cursor = 0
		return m, m.scheduleDetail()
	case k.String() == "G" || k.String() == "end":
		m.cursor = max(0, len(m.visible())-1)
		return m, m.scheduleDetail()
	case key.Matches(k, taskKeyMap.Detail):
		m.showDetail = !m.showDetail
	case key.Matches(k, taskKeyMap.Filter):
		m.filtering = true
		return m, m.filter.Focus()
	case k.String() == "esc":
		if m.showDetail {
			m.showDetail = false
		} else if m.filter.Value() != "" {
			m.filter.SetValue("")
			m.cursor = 0
		}
	case key.Matches(k, taskKeyMap.Mine):
		m.all = !m.all
		m.cursor = 0
		return m, m.load()
	case key.Matches(k, taskKeyMap.States):
		m.stateFilter = (m.stateFilter + 1) % len(stateFilters)
		m.cursor = 0
		return m, m.load()
	case key.Matches(k, taskKeyMap.Refresh):
		return m, m.load()
	case key.Matches(k, taskKeyMap.Start):
		return m, m.startAction()
	case key.Matches(k, taskKeyMap.PR):
		return m, m.prAction()
	case k.String() == "o":
		return m, m.overrideAction()
	case k.String() == "O":
		return m, m.clearOverride()
	case key.Matches(k, taskKeyMap.Unassign):
		return m, m.unassignAction()
	}
	return m, nil
}

// ---------------------------------------------------------------- actions

func (m *tasksScreen) startAction() tea.Cmd {
	t, ok := m.selected()
	if !ok {
		return nil
	}
	ref, err := issueRef(t.Ref)
	if err != nil {
		return toast(toastErr, err.Error())
	}
	if t.State == api.Done {
		return toast(toastWarn, "La tarea está cerrada: reábrela en GitHub para continuar")
	}
	def := domain.BranchTypeFromLabels(t.Labels)
	opts := make([]choice, 0, len(domain.BranchTypes))
	cursor := 0
	for i, bt := range domain.BranchTypes {
		if bt == def {
			cursor = i
		}
		opts = append(opts, choice{label: string(bt), hint: domain.BranchName(bt, ref.Number, t.Title)})
	}
	sh := m.sh
	return openModal(&choiceModal{
		title:    "Empezar " + ref.String(),
		subtitle: t.Title,
		opts:     opts,
		cursor:   cursor,
		onPick: func(i int) tea.Cmd {
			bt := domain.BranchTypes[i]
			fm, cmd := newFlowModal(sh.st, "Empezando "+ref.String(), func(progress func(string)) (string, error) {
				res, err := sh.be.Start(sh.ctx, devflow.StartOptions{Issue: ref, Type: bt, Progress: progress})
				if err != nil {
					return "", err
				}
				if res.Created {
					return "Rama " + res.Branch + " creada. ¡A por ello!", nil
				}
				return "Rama " + res.Branch + " activada.", nil
			})
			return tea.Batch(openModal(fm), cmd)
		},
	})
}

func (m *tasksScreen) prAction() tea.Cmd {
	t, ok := m.selected()
	if !ok {
		return nil
	}
	ref, err := issueRef(t.Ref)
	if err != nil {
		return toast(toastErr, err.Error())
	}
	sh := m.sh
	return openModal(&choiceModal{
		title:    "Abrir PR · " + ref.String(),
		subtitle: t.Title,
		opts: []choice{
			{label: "Lista para revisión", hint: "mueve el Project a In Review"},
			{label: "Borrador (draft)", hint: "se puede marcar lista más tarde"},
		},
		onPick: func(i int) tea.Cmd {
			draft := i == 1
			fm, cmd := newFlowModal(sh.st, "Abriendo PR para "+ref.String(), func(progress func(string)) (string, error) {
				r, err := sh.be.OpenPR(sh.ctx, devflow.PROptions{Issue: &ref, Draft: draft, Progress: progress})
				if err != nil {
					return "", err
				}
				if r.Existing {
					return fmt.Sprintf("Ya existía la PR #%d · %s", r.Number, r.URL), nil
				}
				return fmt.Sprintf("PR #%d abierta · %s", r.Number, r.URL), nil
			})
			return tea.Batch(openModal(fm), cmd)
		},
	})
}

func gitTopLevel() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("el directorio actual no es un repositorio git")
	}
	return strings.TrimSpace(string(out)), nil
}

func (m *tasksScreen) overrideAction() tea.Cmd {
	t, ok := m.selected()
	if !ok {
		return nil
	}
	ref, err := issueRef(t.Ref)
	if err != nil {
		return toast(toastErr, err.Error())
	}
	sh := m.sh
	set := func(scope localapi.OverrideScope) tea.Cmd {
		return func() tea.Msg {
			req := localapi.OverrideRequest{Issue: ref, Scope: scope}
			if scope == localapi.ScopeRepo {
				top, err := gitTopLevel()
				if err != nil {
					return toastMsg{kind: toastErr, text: err.Error()}
				}
				req.RepoPath = top
			}
			if _, err := sh.be.SetOverride(sh.ctx, req); err != nil {
				return errMsg{err}
			}
			return toastMsg{kind: toastOK, text: "Tarea activa: " + ref.String() + " (atribución manual)"}
		}
	}
	return openModal(&choiceModal{
		title:    "Fijar tarea activa",
		subtitle: "Todo el gasto nuevo se imputará a " + ref.String() + ", sin importar la rama.",
		opts: []choice{
			{label: "Solo en este repositorio", hint: "sesiones dentro del repo actual"},
			{label: "Global", hint: "cualquier sesión de esta máquina"},
		},
		onPick: func(i int) tea.Cmd {
			if i == 0 {
				return tea.Sequence(set(localapi.ScopeRepo), statusNow(sh))
			}
			return tea.Sequence(set(localapi.ScopeGlobal), statusNow(sh))
		},
	})
}

func (m *tasksScreen) clearOverride() tea.Cmd {
	sh := m.sh
	return tea.Sequence(func() tea.Msg {
		if err := sh.be.ClearOverride(sh.ctx); err != nil {
			return errMsg{err}
		}
		return toastMsg{kind: toastInfo, text: "Tarea activa eliminada: vuelve a mandar la rama"}
	}, statusNow(sh))
}

func (m *tasksScreen) unassignAction() tea.Cmd {
	t, ok := m.selected()
	if !ok {
		return nil
	}
	ref, err := issueRef(t.Ref)
	if err != nil {
		return toast(toastErr, err.Error())
	}
	sh := m.sh
	return openModal(&confirmModal{
		title: "Devolver a la bandeja",
		body:  fmt.Sprintf("Tus eventos de %s volverán a la bandeja sin asignar para reasignarlos. ¿Continuar?", ref),
		onYes: func() tea.Msg {
			r, err := sh.be.UnassignTask(sh.ctx, ref)
			if err != nil {
				return errMsg{err}
			}
			return tea.BatchMsg{
				toast(toastOK, fmt.Sprintf("%d eventos devueltos a la bandeja", r.Updated)),
				refresh,
			}
		},
	})
}

// ---------------------------------------------------------------- view

func (m *tasksScreen) View(width, height int) string {
	s := m.sh.st
	scope := "mías"
	if m.all {
		scope = "de todo el equipo"
	}
	vis := m.visible()
	var total int64
	for _, t := range vis {
		total += t.CostUsdMicros
	}
	title := s.Title.Render("Tareas") + s.Faint.Render(fmt.Sprintf(" · %s · %s · %d", scope, stateFilters[m.stateFilter].label, len(vis)))
	right := s.Faint.Render("total ") + s.Bold.Render(m.sh.money(total))
	head := spread(title, right, width)
	if m.filtering || m.filter.Value() != "" {
		head += "\n" + m.filter.View()
	}
	bodyH := height - lipgloss.Height(head) - 1

	if m.loading && len(m.tasks) == 0 {
		return head + "\n" + center(s.Faint.Render("Cargando tareas…"), width, bodyH)
	}
	if m.err != nil && len(m.tasks) == 0 {
		return head + "\n" + center(lipgloss.NewStyle().Foreground(s.Danger).Render("✗ "+m.err.Error())+"\n\n"+s.Faint.Render("r para reintentar"), width, bodyH)
	}
	if len(vis) == 0 {
		msg := s.Bold.Render("No hay tareas aquí") + "\n\n" + s.Faint.Render("Prueba con ") + s.Key.Render("a") + s.Faint.Render(" (todas) o ") + s.Key.Render("f") + s.Faint.Render(" (estado).")
		return head + "\n" + center(msg, width, bodyH)
	}

	wide := width >= 110
	listW := width
	if wide {
		listW = width * 58 / 100
	}
	if !wide && m.showDetail {
		return head + "\n" + m.detailView(width, bodyH)
	}
	list := m.listView(vis, listW, bodyH)
	if !wide {
		return head + "\n" + list
	}
	return head + "\n" + lipgloss.JoinHorizontal(lipgloss.Top, list, " ", m.detailView(width-listW-1, bodyH))
}

func (m *tasksScreen) listView(vis []api.Task, width, height int) string {
	s := m.sh.st
	// Build display lines (repo headers + task rows), tracking the cursor line.
	type line struct {
		text string
		task int // -1 for headers
	}
	var lines []line
	repoTotals := map[string]int64{}
	repoCounts := map[string]int{}
	for _, t := range vis {
		repoTotals[t.Ref.Repo] += t.CostUsdMicros
		repoCounts[t.Ref.Repo]++
	}
	costW, tokW, pillW := 10, 7, 11
	titleW := width - 2 - 7 - pillW - costW - tokW - 4
	cursorLine := 0
	prev := ""
	for i, t := range vis {
		if t.Ref.Repo != prev {
			prev = t.Ref.Repo
			h := lipgloss.NewStyle().Foreground(s.Secondary).Bold(true).Render("▍"+shortRepo(t.Ref.Repo, m.sh.org)) +
				s.Faint.Render(fmt.Sprintf("  %d · %s", repoCounts[t.Ref.Repo], m.sh.money(repoTotals[t.Ref.Repo])))
			if len(lines) > 0 {
				lines = append(lines, line{"", -1})
			}
			lines = append(lines, line{h, -1})
		}
		num := s.Faint.Render(padLeft(fmt.Sprintf("#%d", t.Ref.Number), 6))
		title := t.Title
		if t.AdjustmentUsdMicros != nil && *t.AdjustmentUsdMicros != 0 {
			title += " " + lipgloss.NewStyle().Foreground(s.Warning).Render("Δ+"+m.sh.money(*t.AdjustmentUsdMicros)+" tras cierre")
		}
		row := num + " " + pad(title, titleW) + " " + pad(statePill(s, t.State), pillW) + " " +
			padLeft(s.Bold.Render(m.sh.money(t.CostUsdMicros)), costW) + " " +
			padLeft(s.Faint.Render(humanTokens(totalTokens(t.Tokens))), tokW)
		if i == m.cursor {
			cursorLine = len(lines)
			row = lipgloss.NewStyle().Foreground(s.Primary).Bold(true).Render("❯ ") + row
			row = s.Selected.Width(width).Render(row)
		} else {
			row = "  " + row
		}
		lines = append(lines, line{row, i})
	}
	start, end := window(len(lines), cursorLine, height)
	out := make([]string, 0, height)
	for _, l := range lines[start:end] {
		out = append(out, l.text)
	}
	return lipgloss.NewStyle().Width(width).Render(fitHeight(strings.Join(out, "\n"), height))
}

func (m *tasksScreen) detailView(width, height int) string {
	s := m.sh.st
	t, ok := m.selected()
	if !ok {
		return ""
	}
	inner := width - 4
	var b strings.Builder
	b.WriteString(s.Faint.Render(fmt.Sprintf("%s#%d", shortRepo(t.Ref.Repo, m.sh.org), t.Ref.Number)) + "  " + statePill(s, t.State) + "\n")
	b.WriteString(s.Bold.Width(inner).Render(t.Title) + "\n\n")

	cost := lipgloss.NewStyle().Foreground(s.Primary).Bold(true).Render(m.sh.money(t.CostUsdMicros))
	if alt := m.sh.moneyAlt(t.CostUsdMicros); alt != "" {
		cost += s.Faint.Render("  ≈ " + alt)
	}
	b.WriteString(cost + s.Faint.Render(fmt.Sprintf("  · %d peticiones", t.Events)) + "\n")
	if t.SnapshotUsdMicros != nil {
		b.WriteString(s.Faint.Render("congelado al cerrar: "+m.sh.money(*t.SnapshotUsdMicros)) + "\n")
	}
	if t.Branch != nil && *t.Branch != "" {
		b.WriteString(s.Faint.Render("rama ") + s.Base.Render(*t.Branch) + "\n")
	}
	if t.PrNumber != nil {
		b.WriteString(s.Faint.Render("PR ") + s.Base.Render(fmt.Sprintf("#%d", *t.PrNumber)) + "\n")
	}
	if t.StartedAt != nil {
		b.WriteString(s.Faint.Render("empezada "+ago(*t.StartedAt, m.sh.now())) + "\n")
	}
	b.WriteString("\n" + s.Heading.Render("Tokens") + "\n")
	b.WriteString(tokenBars(m.sh, t.Tokens, inner))

	if m.detail != nil && m.detailKey == taskKey(t) {
		d := m.detail
		if len(d.ByModel) > 0 {
			b.WriteString("\n" + s.Heading.Render("Por modelo") + "\n")
			b.WriteString(costRows(m.sh, d.ByModel, inner, 4, shortModel))
		}
		if len(d.ByUser) > 0 {
			b.WriteString("\n" + s.Heading.Render("Por persona") + "\n")
			b.WriteString(costRows(m.sh, d.ByUser, inner, 4, nil))
		}
		if len(d.ByDay) > 1 {
			vals := make([]float64, len(d.ByDay))
			for i, r := range d.ByDay {
				vals[i] = float64(r.CostUsdMicros)
			}
			first, last := d.ByDay[0].Key, d.ByDay[len(d.ByDay)-1].Key
			b.WriteString("\n" + s.Heading.Render("Por día") + "  " + s.Faint.Render(first+" → "+last) + "\n")
			b.WriteString(sparkline(vals, s.Primary) + "\n")
		}
	} else if m.detailErr != nil {
		b.WriteString("\n" + lipgloss.NewStyle().Foreground(s.Danger).Render("✗ "+m.detailErr.Error()))
	} else {
		b.WriteString("\n" + s.Faint.Render("cargando desglose…"))
	}
	content := strings.TrimRight(b.String(), "\n")
	h := min(height, lipgloss.Height(content)+2)
	return s.Card.Width(width).Render(fitHeight(content, h-2))
}

// tokenBars renders a mini bar per token kind.
func tokenBars(sh *shared, t api.Tokens, width int) string {
	s := sh.st
	kinds := []struct {
		label string
		v     int64
	}{
		{"entrada", t.Input}, {"salida", t.Output}, {"razonam.", t.Reasoning}, {"caché lect.", t.CacheRead}, {"caché escr.", t.CacheWrite},
	}
	var maxV int64
	for _, k := range kinds {
		maxV = max(maxV, k.v)
	}
	barW := max(4, width-12-8)
	var b strings.Builder
	for _, k := range kinds {
		if k.v == 0 {
			continue
		}
		b.WriteString(s.Faint.Render(pad(k.label, 12)) + pad(bar(float64(k.v), float64(maxV), barW, s.Gradient), barW) + " " + padLeft(humanTokens(k.v), 7) + "\n")
	}
	if b.Len() == 0 {
		return s.Faint.Render("sin uso todavía") + "\n"
	}
	return b.String()
}

// costRows renders the top n rows with bars.
func costRows(sh *shared, rows []api.CostRow, width, n int, label func(string) string) string {
	s := sh.st
	var maxV int64
	for _, r := range rows {
		maxV = max(maxV, r.CostUsdMicros)
	}
	labelW := min(18, width/3)
	costW := 9
	barW := max(4, width-labelW-costW-2)
	var b strings.Builder
	for i, r := range rows {
		if i >= n {
			b.WriteString(s.Faint.Render(fmt.Sprintf("… y %d más", len(rows)-n)) + "\n")
			break
		}
		l := r.Label
		if label != nil {
			l = label(l)
		}
		b.WriteString(pad(l, labelW) + " " + pad(bar(float64(r.CostUsdMicros), float64(maxV), barW, sh.st.Gradient), barW) + " " + padLeft(sh.money(r.CostUsdMicros), costW) + "\n")
	}
	return b.String()
}

// statusNow fetches the daemon status immediately (after override changes).
func statusNow(sh *shared) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(sh.ctx, 5*time.Second)
		defer cancel()
		st, err := sh.be.Status(ctx)
		return statusMsg{st: st, err: err}
	}
}
