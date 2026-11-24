package tui

import (
	"context"
	"fmt"
	"image/color"
	"os/exec"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/daemon/localapi"
	"github.com/hiberus-industria/forge/internal/devflow"
	"github.com/hiberus-industria/forge/internal/domain"
	"github.com/hiberus-industria/forge/internal/tui/theme"
)

var taskKeys = struct {
	Actions, Detail, Back, Scroll, Filter, Mine, States, Refresh, Start, PR, Pin, Unpin, Unassign, Open, Copy key.Binding
}{
	Actions:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "acciones")),
	Detail:   key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→", "detalle")),
	Back:     key.NewBinding(key.WithKeys("left", "h", "esc"), key.WithHelp("←", "volver")),
	Scroll:   key.NewBinding(key.WithKeys("J", "K"), key.WithHelp("J K", "mover el detalle")),
	Filter:   key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "buscar")),
	Mine:     key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "mías / equipo")),
	States:   key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "cambiar estado")),
	Refresh:  key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "recargar")),
	Start:    key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "empezar")),
	PR:       key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "abrir PR")),
	Pin:      key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "imputar todo aquí")),
	Unpin:    key.NewBinding(key.WithKeys("O"), key.WithHelp("O", "dejar de imputar aquí")),
	Unassign: key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "devolver a la bandeja")),
	Open:     key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "abrir en GitHub")),
	Copy:     key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "copiar enlace")),
}

// stateFilters are the presets cycled with `f`.
var stateFilters = []struct {
	label  string
	states []api.TaskState
}{
	{"Abiertas", []api.TaskState{api.Todo, api.InProgress, api.InReview}},
	{"En curso", []api.TaskState{api.InProgress, api.InReview}},
	{"Cerradas", []api.TaskState{api.Done}},
	{"Todas", nil},
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

	list   rowList[api.Task]
	detail viewport.Model
	width  int
	height int

	info       *api.TaskDetail
	infoKey    string
	infoSeq    int
	infoErr    error
	showDetail bool // narrow layouts: full-screen detail
}

func taskText(t api.Task) string {
	return fmt.Sprintf("%s#%d %s %s", t.Ref.Repo, t.Ref.Number, t.Title, strings.Join(t.Labels, " "))
}

func newTasksScreen(sh *shared) *tasksScreen {
	m := &tasksScreen{sh: sh, loading: true}
	m.list = newRowList(sh.st, taskText)
	m.detail = viewport.New()
	return m
}

func (m *tasksScreen) restyle(s *theme.Styles) { m.list.restyle(s) }

func (m *tasksScreen) Hints() []key.Binding {
	switch {
	case m.list.Filtering():
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "aplicar")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "quitar")),
		}
	case m.showDetail && !m.wide():
		return []key.Binding{taskKeys.Back, taskKeys.Actions, navKeys.UpDown}
	case !m.wide():
		return []key.Binding{taskKeys.Actions, taskKeys.Detail, taskKeys.Start, taskKeys.PR, taskKeys.Filter}
	}
	return []key.Binding{taskKeys.Actions, taskKeys.Start, taskKeys.PR, taskKeys.Filter, taskKeys.Mine}
}

func (m *tasksScreen) Help() []helpGroup {
	move := []key.Binding{navKeys.Up, navKeys.Down, navKeys.PgUp, navKeys.PgDown}
	if m.wide() {
		move = append(move, taskKeys.Scroll)
	} else {
		move = append(move, taskKeys.Detail, taskKeys.Back)
	}
	return []helpGroup{
		{"Moverse", move},
		{"Sobre la tarea", []key.Binding{taskKeys.Actions, taskKeys.Start, taskKeys.PR, taskKeys.Pin, taskKeys.Unpin, taskKeys.Unassign, taskKeys.Open, taskKeys.Copy}},
		{"Lista", []key.Binding{taskKeys.Filter, taskKeys.Mine, taskKeys.States, taskKeys.Refresh}},
	}
}

func (m *tasksScreen) Capturing() bool { return m.list.Filtering() }
func (m *tasksScreen) Init() tea.Cmd   { return m.load() }

// wide reports whether list and detail fit side by side.
func (m *tasksScreen) wide() bool { return m.width >= 100 }

// listWidth is the list column width in the wide layout.
func (m *tasksScreen) listWidth() int {
	if !m.wide() {
		return m.width
	}
	return m.width * 56 / 100
}

// Body rows: toolbar, gap, then the list (and detail).
const tasksChrome = 2

func (m *tasksScreen) SetSize(w, h int) {
	m.width, m.height = w, h
	m.list.SetHeight(h - tasksChrome)
	if m.wide() {
		m.detail.SetWidth(w - m.listWidth() - 3)
	} else {
		m.detail.SetWidth(w)
	}
	m.detail.SetHeight(max(1, h-tasksChrome))
	m.syncDetail()
}

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

// sortTasks orders tasks by repo, state and recency.
func sortTasks(ts []api.Task) []api.Task {
	out := append([]api.Task(nil), ts...)
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

// visible returns the tasks the list shows, in order (filter applied).
func (m *tasksScreen) visible() []api.Task { return m.list.Visible() }

func (m *tasksScreen) selected() (api.Task, bool) { return m.list.Selected() }

func taskKey(t api.Task) string { return fmt.Sprintf("%s#%d", t.Ref.Repo, t.Ref.Number) }

// scheduleDetail debounces detail fetching while the cursor moves.
func (m *tasksScreen) scheduleDetail() tea.Cmd {
	t, ok := m.selected()
	if !ok || taskKey(t) == m.infoKey {
		return nil
	}
	m.infoSeq++
	seq := m.infoSeq
	m.detail.GotoTop()
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return detailDebounceMsg{seq} })
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
	cmd := m.update(msg)
	m.syncDetail()
	return m, cmd
}

func (m *tasksScreen) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tasksLoadedMsg:
		if msg.seq != m.seq {
			return nil
		}
		m.loading = false
		m.err = msg.err
		if msg.err != nil {
			return nil
		}
		m.tasks = sortTasks(msg.tasks)
		for _, t := range m.tasks {
			m.sh.titles[taskKey(t)] = t.Title
		}
		m.list.SetItems(m.tasks, taskKey)
		m.infoKey = ""
		return m.scheduleDetail()
	case detailDebounceMsg:
		if msg.seq == m.infoSeq {
			if t, ok := m.selected(); ok {
				return m.fetchDetail(t)
			}
		}
		return nil
	case taskDetailMsg:
		if t, ok := m.selected(); ok && taskKey(t) == msg.key {
			m.infoKey, m.infoErr = msg.key, msg.err
			if msg.err == nil {
				d := msg.detail
				m.info = &d
			}
		}
		return nil
	case refreshMsg:
		m.infoKey = ""
		return m.load()
	case tea.MouseWheelMsg:
		if m.showDetail || (m.wide() && msg.X > m.listWidth()) {
			var cmd tea.Cmd
			m.detail, cmd = m.detail.Update(msg)
			return cmd
		}
		m.list.Wheel(msg)
		return m.scheduleDetail()
	case tea.MouseClickMsg:
		if !m.showDetail && msg.X < m.listWidth() && m.list.Click(msg.Y-tasksChrome) {
			return m.scheduleDetail()
		}
		return nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	if m.list.Filtering() {
		var cmd tea.Cmd
		m.list.input, cmd = m.list.input.Update(msg)
		return cmd
	}
	return nil
}

func (m *tasksScreen) handleKey(k tea.KeyPressMsg) tea.Cmd {
	if m.list.Filtering() {
		return tea.Batch(m.list.FilterKey(k), m.scheduleDetail())
	}
	if m.showDetail && !m.wide() {
		switch {
		case key.Matches(k, taskKeys.Back):
			m.showDetail = false
			return nil
		case key.Matches(k, navKeys.Up, navKeys.Down, navKeys.PgUp, navKeys.PgDown):
			var cmd tea.Cmd
			m.detail, cmd = m.detail.Update(k)
			return cmd
		}
	}
	switch {
	case k.String() == "esc" && m.list.Query() != "":
		m.list.ClearFilter()
		return m.scheduleDetail()
	case key.Matches(k, taskKeys.Filter):
		return m.list.StartFilter()
	case key.Matches(k, taskKeys.Detail):
		if !m.wide() {
			m.showDetail = true
			m.detail.GotoTop()
		}
		return nil
	case key.Matches(k, taskKeys.Scroll):
		if k.String() == "J" {
			m.detail.ScrollDown(3)
		} else {
			m.detail.ScrollUp(3)
		}
		return nil
	case key.Matches(k, taskKeys.Actions):
		return m.actionsMenu()
	case key.Matches(k, taskKeys.Mine):
		m.all = !m.all
		m.list.Top()
		return m.load()
	case key.Matches(k, taskKeys.States):
		m.stateFilter = (m.stateFilter + 1) % len(stateFilters)
		m.list.Top()
		return m.load()
	case key.Matches(k, taskKeys.Refresh):
		return m.load()
	}
	if cmd, ok := m.runAction(k.String()); ok {
		return cmd
	}
	if m.list.Nav(k) {
		return m.scheduleDetail()
	}
	return nil
}

// ---------------------------------------------------------------- actions

// actions are what can be done with the selected task. The menu (enter)
// and the direct keys share this list, so both always agree.
func (m *tasksScreen) actions(t api.Task) []choice {
	var start, unassign string
	if t.State == api.Done {
		start = "la tarea está cerrada"
	}
	if t.CostUsdMicros == 0 {
		unassign = "no tiene gasto"
	}
	pinned := false
	if st := m.sh.status; st != nil && st.Override != nil {
		pinned = st.Override.Issue.Repo.String() == t.Ref.Repo && st.Override.Issue.Number == t.Ref.Number
	}
	pin := choice{key: "o", label: "Imputar todo el gasto aquí", hint: "aunque cambies de rama"}
	if pinned {
		pin = choice{key: "O", label: "Dejar de imputar aquí", hint: "vuelve a mandar la rama"}
	}
	return []choice{
		{key: "s", label: "Empezar a trabajar", hint: "crea su rama", disabled: start},
		{key: "p", label: "Abrir pull request", hint: "y pasarla a revisión"},
		pin,
		{key: "u", label: "Devolver gasto a la bandeja", hint: "si se imputó por error", disabled: unassign},
		{key: "w", label: "Abrir en GitHub"},
		{key: "y", label: "Copiar enlace"},
	}
}

func (m *tasksScreen) actionsMenu() tea.Cmd {
	t, ok := m.selected()
	if !ok {
		return nil
	}
	opts := m.actions(t)
	return openModal(&choiceModal{
		title:    fmt.Sprintf("%s#%d", shortRepo(t.Ref.Repo, m.sh.org), t.Ref.Number),
		subtitle: t.Title,
		opts:     opts,
		onPick: func(i int) tea.Cmd {
			cmd, _ := m.runAction(opts[i].key)
			return cmd
		},
	})
}

// runAction runs the action bound to k on the selected task.
func (m *tasksScreen) runAction(k string) (tea.Cmd, bool) {
	t, ok := m.selected()
	if !ok {
		return nil, false
	}
	for _, a := range m.actions(t) {
		if a.key != k {
			continue
		}
		if a.disabled != "" {
			return toast(toastWarn, "No disponible: "+a.disabled), true
		}
		switch k {
		case "s":
			return m.startAction(t), true
		case "p":
			return m.prAction(t), true
		case "o":
			return m.pinAction(t), true
		case "O":
			return m.clearPin(), true
		case "u":
			return m.unassignAction(t), true
		case "w":
			return openURL(t.Url), true
		case "y":
			return copyText(t.Url, "Enlace"), true
		}
	}
	return nil, false
}

func (m *tasksScreen) startAction(t api.Task) tea.Cmd {
	ref, err := issueRef(t.Ref)
	if err != nil {
		return toast(toastErr, err.Error())
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
		title:    "¿Qué tipo de trabajo es?",
		subtitle: "Crea la rama y mueve " + ref.String() + " a En curso.",
		opts:     opts,
		cursor:   cursor,
		onPick: func(i int) tea.Cmd {
			bt := domain.BranchTypes[i]
			fm, cmd := newFlowModal(sh.st, "Empezando "+ref.String(), func(progress func(string)) (flowResult, error) {
				res, err := sh.be.Start(sh.ctx, devflow.StartOptions{Issue: ref, Type: bt, Progress: progress})
				if err != nil {
					return flowResult{}, err
				}
				if res.Created {
					return flowResult{text: "Listo. Estás en la rama nueva " + res.Branch + "."}, nil
				}
				return flowResult{text: "Listo. Has vuelto a la rama " + res.Branch + "."}, nil
			})
			return tea.Batch(openModal(fm), cmd)
		},
	})
}

func (m *tasksScreen) prAction(t api.Task) tea.Cmd {
	ref, err := issueRef(t.Ref)
	if err != nil {
		return toast(toastErr, err.Error())
	}
	sh := m.sh
	return openModal(&choiceModal{
		title:    "Abrir pull request",
		subtitle: ref.String() + " · " + t.Title,
		opts: []choice{
			{label: "Lista para revisar", hint: "pasa la tarea a En revisión"},
			{label: "Borrador", hint: "la marcas como lista más tarde"},
		},
		onPick: func(i int) tea.Cmd {
			draft := i == 1
			fm, cmd := newFlowModal(sh.st, "Abriendo la PR de "+ref.String(), func(progress func(string)) (flowResult, error) {
				r, err := sh.be.OpenPR(sh.ctx, devflow.PROptions{Issue: &ref, Draft: draft, Progress: progress})
				if err != nil {
					return flowResult{}, err
				}
				label := fmt.Sprintf("%s#%d", ref.Repo, r.Number)
				if r.Existing {
					return flowResult{text: fmt.Sprintf("La PR #%d ya existía.", r.Number), url: r.URL, urlLabel: label}, nil
				}
				return flowResult{text: fmt.Sprintf("PR #%d abierta.", r.Number), url: r.URL, urlLabel: label}, nil
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

func (m *tasksScreen) pinAction(t api.Task) tea.Cmd {
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
			return toastMsg{kind: toastOK, text: "Desde ahora, el gasto nuevo va a " + ref.String()}
		}
	}
	return openModal(&choiceModal{
		title:    "Imputar todo el gasto a " + ref.String(),
		subtitle: "Útil cuando trabajas fuera de la rama de la tarea. Se deshace con O.",
		opts: []choice{
			{label: "Solo en este repositorio", hint: "sesiones abiertas dentro del repo actual"},
			{label: "En toda la máquina", hint: "cualquier sesión"},
		},
		onPick: func(i int) tea.Cmd {
			if i == 0 {
				return tea.Sequence(set(localapi.ScopeRepo), statusNow(sh))
			}
			return tea.Sequence(set(localapi.ScopeGlobal), statusNow(sh))
		},
	})
}

func (m *tasksScreen) clearPin() tea.Cmd {
	sh := m.sh
	return tea.Sequence(func() tea.Msg {
		if err := sh.be.ClearOverride(sh.ctx); err != nil {
			return errMsg{err}
		}
		return toastMsg{kind: toastInfo, text: "Hecho: el gasto vuelve a imputarse según la rama"}
	}, statusNow(sh))
}

func (m *tasksScreen) unassignAction(t api.Task) tea.Cmd {
	ref, err := issueRef(t.Ref)
	if err != nil {
		return toast(toastErr, err.Error())
	}
	sh := m.sh
	return openModal(&confirmModal{
		title: "¿Devolver tu gasto a la bandeja?",
		body:  fmt.Sprintf("Tus peticiones de %s pasarán a la bandeja, donde podrás asignarlas a otra tarea. El gasto de otras personas no se toca.", ref),
		onYes: func() tea.Msg {
			r, err := sh.be.UnassignTask(sh.ctx, ref)
			if err != nil {
				return errMsg{err}
			}
			return tea.BatchMsg{
				toast(toastOK, plural(r.Updated, "petición devuelta", "peticiones devueltas")+" a la bandeja"),
				refresh,
			}
		},
	})
}

// ---------------------------------------------------------------- view

func (m *tasksScreen) toolbar() string {
	s := m.sh.st
	if m.list.Filtering() || m.list.Query() != "" {
		return m.list.FilterView(s, m.width)
	}
	scope := 0
	if m.all {
		scope = 1
	}
	labels := make([]string, len(stateFilters))
	for i, f := range stateFilters {
		labels[i] = f.label
	}
	left := segmented(s, []string{"Mías", "Equipo"}, scope) + "     " + segmented(s, labels, m.stateFilter)
	var total int64
	for _, t := range m.visible() {
		total += t.CostUsdMicros
	}
	right := s.Faint.Render(plural(m.list.Len(), "tarea", "tareas")+"  ") + s.Bold.Render(m.sh.money(total))
	if m.loading && len(m.tasks) > 0 {
		right = s.Faint.Render("actualizando…  ") + right
	}
	return spread(left, right, m.width)
}

func (m *tasksScreen) View() string {
	s := m.sh.st
	width, bodyH := m.width, max(1, m.height-tasksChrome)
	head := m.toolbar() + "\n\n"

	switch {
	case m.loading && len(m.tasks) == 0:
		return head + center(s.Faint.Render("Cargando tareas…"), width, bodyH)
	case m.err != nil && len(m.tasks) == 0:
		return head + emptyState(s, width, bodyH, s.Err.Render("No se pudieron cargar las tareas"), m.err.Error(), "Pulsa r para reintentar.")
	case len(m.tasks) == 0:
		if m.all {
			return head + emptyState(s, width, bodyH, "No hay tareas con este estado", "Pulsa f para ver otro estado.")
		}
		return head + emptyState(s, width, bodyH, "No tienes tareas asignadas con este estado",
			"Pulsa a para ver las del equipo o f para cambiar el estado.",
			"Para empezar una desde la terminal: forge start <número>")
	case m.list.Len() == 0:
		return head + emptyState(s, width, bodyH, "Ninguna tarea coincide con la búsqueda", "Pulsa esc para quitar el filtro.")
	}

	if !m.wide() {
		if m.showDetail {
			return head + m.detail.View()
		}
		return head + m.listView(width)
	}
	lw := m.listWidth()
	sep := s.Rule.Render(strings.TrimSuffix(strings.Repeat("│\n", bodyH), "\n"))
	return head + lipgloss.JoinHorizontal(lipgloss.Top, padLines(m.listView(lw), lw), " ", sep, "  ", m.detail.View())
}

func (m *tasksScreen) listView(width int) string {
	s := m.sh.st
	refW := 0
	for _, t := range m.list.Visible() {
		refW = max(refW, lipgloss.Width(fmt.Sprintf("%s#%d", shortRepo(t.Ref.Repo, m.sh.org), t.Ref.Number)))
	}
	terms := m.list.Terms()
	return m.list.View(width, func(t api.Task, sel bool, w int) string {
		g, gs := stateGlyph(s, t.State)
		titleSt := s.Base
		if sel {
			titleSt = s.Bold.Background(s.Surface)
		}
		cost, costSt := m.sh.money(t.CostUsdMicros), s.Base
		if t.CostUsdMicros == 0 {
			cost, costSt = "—", s.Dim
		}
		return row(s, w, sel,
			col{text: g, w: 1, st: gs},
			col{text: fmt.Sprintf("%s#%d", shortRepo(t.Ref.Repo, m.sh.org), t.Ref.Number), w: refW, st: s.Faint},
			col{text: mark(t.Title, terms, titleSt), raw: true},
			col{text: cost, w: 9, right: true, st: costSt},
		)
	})
}

// syncDetail refreshes the detail viewport content for the selected task.
func (m *tasksScreen) syncDetail() {
	if m.detail.Width() <= 0 {
		return
	}
	m.detail.SetContent(m.detailContent(m.detail.Width()))
}

// prURL derives the pull request URL from the issue URL.
func prURL(issueURL string, n int) string {
	if i := strings.LastIndex(issueURL, "/issues/"); i >= 0 {
		return fmt.Sprintf("%s/pull/%d", issueURL[:i], n)
	}
	return ""
}

func (m *tasksScreen) detailContent(width int) string {
	s := m.sh.st
	t, ok := m.selected()
	if !ok {
		return ""
	}
	var lines []string
	add := func(l ...string) { lines = append(lines, l...) }
	ref := fmt.Sprintf("%s#%d", shortRepo(t.Ref.Repo, m.sh.org), t.Ref.Number)

	add(link(s, t.Url, ref) + "   " + stateLabel(s, t.State))
	add(strings.Split(s.Bold.Width(width).Render(t.Title), "\n")...)
	add("")
	cost := s.Hi.Render(m.sh.money(t.CostUsdMicros))
	if alt := m.sh.moneyAlt(t.CostUsdMicros); alt != "" && t.CostUsdMicros > 0 {
		cost += s.Faint.Render("  ≈ " + alt)
	}
	add(cost)
	add(s.Faint.Render(plural(t.Events, "petición", "peticiones") + " · " + humanTokens(totalTokens(t.Tokens)) + " tokens"))
	if t.SnapshotUsdMicros != nil {
		add(s.Faint.Render("Al cerrarla costaba ") + s.Base.Render(m.sh.money(*t.SnapshotUsdMicros)))
	}
	if t.AdjustmentUsdMicros != nil && *t.AdjustmentUsdMicros != 0 {
		add(s.Warn.Render("+" + m.sh.money(*t.AdjustmentUsdMicros) + " después de cerrarla"))
	}

	add("")
	kv := func(k, v string) { add(s.Faint.Render(pad(k, 8)) + trunc(v, width-8)) }
	if t.Branch != nil && *t.Branch != "" {
		kv("Rama", s.Base.Render(*t.Branch))
	}
	if t.PrNumber != nil {
		kv("PR", link(s, prURL(t.Url, *t.PrNumber), fmt.Sprintf("#%d", *t.PrNumber)))
	}
	if t.StartedAt != nil {
		kv("Inicio", s.Base.Render(ago(*t.StartedAt, m.sh.now())))
	}
	if t.LastActivityAt != nil {
		kv("Último", s.Base.Render(ago(*t.LastActivityAt, m.sh.now())))
	}
	if len(t.Assignees) > 0 {
		kv("Equipo", s.Base.Render("@"+strings.Join(t.Assignees, " @")))
	}

	if totalTokens(t.Tokens) > 0 {
		add("", s.Section.Render("Tokens"))
		add(tokenBreakdown(s, t.Tokens, width)...)
	}

	switch {
	case m.info != nil && m.infoKey == taskKey(t):
		d := m.info
		if len(d.ByModel) > 0 {
			add("", s.Section.Render("Por modelo"))
			add(costRows(m.sh, d.ByModel, width, 4, shortModel)...)
		}
		if len(d.ByUser) > 1 {
			add("", s.Section.Render("Por persona"))
			add(costRows(m.sh, d.ByUser, width, 4, nil)...)
		}
		if len(d.ByDay) > 1 {
			vals := make([]float64, len(d.ByDay))
			for i, r := range d.ByDay {
				vals[i] = float64(r.CostUsdMicros)
			}
			first, last := dayLabel(d.ByDay[0].Key), dayLabel(d.ByDay[len(d.ByDay)-1].Key)
			add("", spread(s.Section.Render("Por día"), s.Dim.Render(first+" → "+last), width))
			add(sparkline(vals, s.Primary, s.Border))
		}
	case m.infoErr != nil:
		add("", s.Err.Render(m.infoErr.Error()))
	case t.CostUsdMicros > 0:
		add("", s.Dim.Render("Cargando desglose…"))
	}
	return strings.Join(lines, "\n")
}

// dayLabel renders an API day key (2006-01-02) as "14 oct".
func dayLabel(k string) string {
	if d, err := time.Parse("2006-01-02", k); err == nil {
		return dayMonth(d)
	}
	return k
}

// tokenBreakdown renders the token mix as one stacked bar plus a legend.
func tokenBreakdown(s *theme.Styles, t api.Tokens, width int) []string {
	kinds := []struct {
		label string
		v     int64
		c     color.Color
	}{
		{"entrada", t.Input, s.Primary},
		{"salida", t.Output, s.Accent},
		{"razonamiento", t.Reasoning, s.Secondary},
		{"caché leída", t.CacheRead, s.Info},
		{"caché escrita", t.CacheWrite, s.Warning},
	}
	total := float64(totalTokens(t))
	var vals []float64
	var cols []color.Color
	var legend []string
	for _, k := range kinds {
		if k.v == 0 {
			continue
		}
		vals = append(vals, float64(k.v))
		cols = append(cols, k.c)
		legend = append(legend, lipgloss.NewStyle().Foreground(k.c).Render("■")+" "+
			s.Faint.Render(fmt.Sprintf("%s %.0f%%", k.label, float64(k.v)/total*100)))
	}
	// The legend wraps by item, never in the middle of one.
	out := []string{stacked(vals, cols, width)}
	line := ""
	for _, l := range legend {
		switch {
		case line == "":
			line = l
		case lipgloss.Width(line)+3+lipgloss.Width(l) > width:
			out = append(out, line)
			line = l
		default:
			line += "   " + l
		}
	}
	return append(out, line)
}

// costRows renders the top n rows as shares of their total.
func costRows(sh *shared, rows []api.CostRow, width, n int, label func(string) string) []string {
	s := sh.st
	var total, top int64
	for _, r := range rows {
		total += r.CostUsdMicros
		top = max(top, r.CostUsdMicros)
	}
	labelW, costW := min(14, width/3), 9
	barW := max(4, width-labelW-costW-4)
	var out []string
	for i, r := range rows {
		if i >= n {
			out = append(out, s.Dim.Render(fmt.Sprintf("y %d más", len(rows)-n)))
			break
		}
		l := r.Label
		if label != nil {
			l = label(l)
		}
		frac := 0.0
		if top > 0 {
			frac = float64(r.CostUsdMicros) / float64(top)
		}
		out = append(out, s.Base.Render(pad(l, labelW))+"  "+meter(s, frac, barW, s.Primary)+"  "+s.Base.Render(padLeft(sh.money(r.CostUsdMicros), costW)))
	}
	return out
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
