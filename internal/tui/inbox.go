package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/domain"
	"github.com/hiberus-industria/forge/internal/tui/theme"
)

var inboxKeys = struct {
	Toggle, All, Assign, Clear, Filter, Mine, Refresh key.Binding
}{
	Toggle:  key.NewBinding(key.WithKeys("space", "x"), key.WithHelp("espacio", "marcar")),
	All:     key.NewBinding(key.WithKeys("A"), key.WithHelp("A", "marcar todas")),
	Assign:  key.NewBinding(key.WithKeys("enter", "m"), key.WithHelp("enter", "asignar a una tarea")),
	Clear:   key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "desmarcar")),
	Filter:  key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "buscar")),
	Mine:    key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "mía / equipo")),
	Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "recargar")),
}

type inboxLoadedMsg struct {
	seq    int
	groups []api.InboxGroup
	err    error
}

type pickTasksMsg struct {
	tasks []api.Task
	err   error
}

type reassignDoneMsg struct {
	res api.ReassignResult
	to  domain.IssueRef
}

func groupWhere(g api.InboxGroup, org string) string {
	if g.Bucket == api.BucketRepo && g.Repo != nil {
		return shortRepo(*g.Repo, org)
	}
	return "sin repositorio"
}

func groupModels(g api.InboxGroup) string {
	if g.Models == nil {
		return ""
	}
	ms := make([]string, len(*g.Models))
	for i, x := range *g.Models {
		ms[i] = shortModel(x)
	}
	return strings.Join(ms, ", ")
}

func groupWho(g api.InboxGroup) string {
	if g.User.GithubLogin != nil {
		return "@" + *g.User.GithubLogin
	}
	return g.User.Name
}

type inboxScreen struct {
	sh       *shared
	groups   []api.InboxGroup
	loading  bool
	err      error
	seq      int
	selected map[string]bool
	all      bool

	list          rowList[api.InboxGroup]
	width, height int
}

func newInboxScreen(sh *shared) *inboxScreen {
	m := &inboxScreen{sh: sh, selected: map[string]bool{}, loading: true}
	m.list = newRowList(sh.st, func(g api.InboxGroup) string {
		b := ""
		if g.Branch != nil {
			b = *g.Branch
		}
		return strings.Join([]string{groupWhere(g, sh.org), b, harnessName(string(g.Harness)), groupModels(g), groupWho(g)}, " ")
	})
	return m
}

func (m *inboxScreen) restyle(s *theme.Styles) { m.list.restyle(s) }

// Badge is the number of sessions waiting in the inbox.
func (m *inboxScreen) Badge() int { return len(m.groups) }

func (m *inboxScreen) Hints() []key.Binding {
	switch {
	case m.list.Filtering():
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "aplicar")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "quitar")),
		}
	case len(m.selected) > 0:
		return []key.Binding{inboxKeys.Assign, inboxKeys.Toggle, inboxKeys.Clear}
	}
	return []key.Binding{inboxKeys.Assign, inboxKeys.Toggle, inboxKeys.Filter, inboxKeys.Mine}
}

func (m *inboxScreen) Help() []helpGroup {
	return []helpGroup{
		{"Moverse", []key.Binding{navKeys.Up, navKeys.Down, navKeys.PgUp, navKeys.PgDown}},
		{"Asignar", []key.Binding{inboxKeys.Toggle, inboxKeys.All, inboxKeys.Clear, inboxKeys.Assign}},
		{"Lista", []key.Binding{inboxKeys.Filter, inboxKeys.Mine, inboxKeys.Refresh}},
	}
}

func (m *inboxScreen) Capturing() bool { return m.list.Filtering() }
func (m *inboxScreen) Init() tea.Cmd   { return m.load() }

// Body rows: toolbar, explanation, gap, column titles, list, selection bar.
const (
	inboxTop    = 4
	inboxBottom = 1
)

func (m *inboxScreen) SetSize(w, h int) {
	m.width, m.height = w, h
	m.list.SetHeight(h - inboxTop - inboxBottom)
}

// groupKey identifies an inbox group across reloads.
func groupKey(g api.InboxGroup) string {
	repo, branch := "", ""
	if g.Repo != nil {
		repo = *g.Repo
	}
	if g.Branch != nil {
		branch = *g.Branch
	}
	return strings.Join([]string{string(g.Harness), g.SessionId, string(g.Bucket), repo, branch}, "|")
}

func (m *inboxScreen) load() tea.Cmd {
	m.seq++
	seq, all, be, ctx := m.seq, m.all, m.sh.be, m.sh.ctx
	m.loading = true
	return func() tea.Msg {
		p := api.ListInboxParams{}
		if !all {
			me := "me"
			p.User = &me
		}
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		gs, err := be.Inbox(ctx, p)
		return inboxLoadedMsg{seq: seq, groups: gs, err: err}
	}
}

// selection returns the marked groups, or the one under the cursor when
// nothing is marked.
func (m *inboxScreen) selection() []api.InboxGroup {
	var out []api.InboxGroup
	for _, g := range m.groups {
		if m.selected[groupKey(g)] {
			out = append(out, g)
		}
	}
	if g, ok := m.list.Selected(); ok && len(out) == 0 {
		out = append(out, g)
	}
	return out
}

func (m *inboxScreen) selectedCost() int64 {
	var c int64
	for _, g := range m.groups {
		if m.selected[groupKey(g)] {
			c += g.CostUsdMicros
		}
	}
	return c
}

// buildReassign builds the API request moving every event of the groups to
// the target issue (nil = back to the inbox).
func buildReassign(groups []api.InboxGroup, to *domain.IssueRef, reason string) (api.ReassignRequest, int64) {
	var req api.ReassignRequest
	var cost int64
	seen := map[string]bool{}
	for _, g := range groups {
		cost += g.CostUsdMicros
		for _, id := range g.EventIds {
			if seen[id.String()] {
				continue
			}
			seen[id.String()] = true
			req.EventIds = append(req.EventIds, id)
		}
	}
	if to != nil {
		req.To = &api.IssueRef{Repo: to.Repo.String(), Number: to.Number}
	}
	if reason != "" {
		req.Reason = &reason
	}
	return req, cost
}

func (m *inboxScreen) toggle(g api.InboxGroup) {
	k := groupKey(g)
	if m.selected[k] {
		delete(m.selected, k)
	} else {
		m.selected[k] = true
	}
}

func (m *inboxScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case inboxLoadedMsg:
		if msg.seq != m.seq {
			return m, nil
		}
		m.loading, m.err = false, msg.err
		if msg.err != nil {
			return m, nil
		}
		m.groups = msg.groups
		keep := map[string]bool{}
		for _, g := range m.groups {
			if k := groupKey(g); m.selected[k] {
				keep[k] = true
			}
		}
		m.selected = keep
		m.list.SetItems(m.groups, groupKey)
		return m, nil
	case refreshMsg:
		return m, m.load()
	case pickTasksMsg:
		return m, m.openPicker(msg)
	case reassignDoneMsg:
		m.selected = map[string]bool{}
		text := fmt.Sprintf("%s a %s", plural(msg.res.Updated, "petición asignada", "peticiones asignadas"), msg.to)
		kind := toastOK
		if msg.res.Skipped > 0 {
			text += fmt.Sprintf(" · %d no eran tuyas y no se han movido", msg.res.Skipped)
			kind = toastWarn
		}
		return m, tea.Batch(toast(kind, text), refresh)
	case tea.MouseWheelMsg:
		m.list.Wheel(msg)
		return m, nil
	case tea.MouseClickMsg:
		// A click selects the row; on the mark it also toggles it.
		if m.list.Click(msg.Y-inboxTop) && msg.X < 4 {
			if g, ok := m.list.Selected(); ok {
				m.toggle(g)
			}
		}
		return m, nil
	case tea.KeyPressMsg:
		if m.list.Filtering() {
			return m, m.list.FilterKey(msg)
		}
		switch {
		case key.Matches(msg, inboxKeys.Toggle):
			if g, ok := m.list.Selected(); ok {
				m.toggle(g)
				m.list.Move(1)
			}
			return m, nil
		case key.Matches(msg, inboxKeys.All):
			if len(m.selected) == m.list.Len() {
				m.selected = map[string]bool{}
			} else {
				for _, g := range m.list.Visible() {
					m.selected[groupKey(g)] = true
				}
			}
			return m, nil
		case key.Matches(msg, inboxKeys.Clear):
			if len(m.selected) > 0 {
				m.selected = map[string]bool{}
			} else {
				m.list.ClearFilter()
			}
			return m, nil
		case key.Matches(msg, inboxKeys.Filter):
			return m, m.list.StartFilter()
		case key.Matches(msg, inboxKeys.Mine):
			m.all = !m.all
			m.selected = map[string]bool{}
			m.list.Top()
			return m, m.load()
		case key.Matches(msg, inboxKeys.Refresh):
			return m, m.load()
		case key.Matches(msg, inboxKeys.Assign):
			if len(m.groups) == 0 {
				return m, nil
			}
			be, ctx := m.sh.be, m.sh.ctx
			return m, func() tea.Msg {
				all := "all"
				ts, err := be.Tasks(ctx, api.ListTasksParams{Assignee: &all, State: &[]api.TaskState{api.Todo, api.InProgress, api.InReview}})
				return pickTasksMsg{tasks: ts, err: err}
			}
		}
		m.list.Nav(msg)
		return m, nil
	}
	if m.list.Filtering() {
		var cmd tea.Cmd
		m.list.input, cmd = m.list.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *inboxScreen) openPicker(msg pickTasksMsg) tea.Cmd {
	groups := m.selection()
	if len(groups) == 0 {
		return nil
	}
	sh := m.sh
	// Tasks of the same repository first: they are the likely target.
	repoHint := ""
	if groups[0].Repo != nil {
		repoHint = *groups[0].Repo
	}
	var items []pickItem
	for pass := range 2 {
		for _, t := range msg.tasks {
			same := t.Ref.Repo == repoHint
			if (pass == 0) != same {
				continue
			}
			ref, err := issueRef(t.Ref)
			if err != nil {
				continue
			}
			items = append(items, pickItem{ref: ref, title: t.Title, suggested: same})
		}
	}
	req, cost := buildReassign(groups, nil, "")
	sub := fmt.Sprintf("%s · %s · %s", plural(len(groups), "sesión", "sesiones"), plural(len(req.EventIds), "petición", "peticiones"), sh.money(cost))
	if msg.err != nil {
		sub += ". No se pudieron cargar las tareas: escribe repo#número."
	}
	return openModal(newPickerModal(sh.st, "¿A qué tarea va este gasto?", sub, sh.org, items, func(ref domain.IssueRef) tea.Cmd {
		req, _ := buildReassign(groups, &ref, "triaje desde la bandeja")
		return func() tea.Msg {
			res, err := sh.be.Reassign(sh.ctx, req)
			if err != nil {
				return errMsg{err}
			}
			return reassignDoneMsg{res: res, to: ref}
		}
	}))
}

// inboxCols decides which columns fit: the less important ones go first.
type inboxCols struct {
	where, branch, harness, models, who int
}

func (m *inboxScreen) columns(width int) inboxCols {
	c := inboxCols{where: 15, branch: 22, harness: 11, models: 18}
	if m.all {
		c.who = 10
	}
	// check, events, last, cost and gaps are always there.
	fixed := 2 + 1 + 5 + 9 + 9 + colGap*4
	sum := func() int {
		n := fixed + c.where + c.branch
		for _, w := range []int{c.harness, c.models, c.who} {
			if w > 0 {
				n += w + colGap
			}
		}
		return n + colGap
	}
	for _, drop := range []*int{&c.models, &c.harness, &c.who} {
		if sum() <= width {
			break
		}
		*drop = 0
	}
	c.branch = max(8, c.branch+width-sum())
	return c
}

func (m *inboxScreen) View() string {
	s := m.sh.st
	width, height := m.width, m.height
	scope := 0
	if m.all {
		scope = 1
	}
	var total int64
	for _, g := range m.groups {
		total += g.CostUsdMicros
	}
	toolbar := spread(segmented(s, []string{"Mía", "Equipo"}, scope),
		s.Faint.Render(plural(len(m.groups), "sesión", "sesiones")+" sin asignar  ")+s.Bold.Render(m.sh.money(total)), width)
	if m.list.Filtering() || m.list.Query() != "" {
		toolbar = m.list.FilterView(s, width)
	}
	intro := s.Faint.Render(trunc("Gasto que no se pudo asociar a una tarea: sesiones fuera de la rama de un issue.", width))
	bodyH := max(1, height-inboxTop)

	switch {
	case m.loading && len(m.groups) == 0:
		return toolbar + "\n" + intro + "\n\n" + center(s.Faint.Render("Cargando bandeja…"), width, bodyH)
	case m.err != nil && len(m.groups) == 0:
		return toolbar + "\n" + intro + "\n\n" + emptyState(s, width, bodyH, s.Err.Render("No se pudo cargar la bandeja"), m.err.Error(), "Pulsa r para reintentar.")
	case len(m.groups) == 0:
		return toolbar + "\n\n\n" + emptyState(s, width, bodyH, s.OK.Render("✓ ")+s.Bold.Render("Bandeja vacía"), "Todo tu gasto está asignado a una tarea.")
	}

	c := m.columns(width)
	cols := func(g *api.InboxGroup, sel bool) []col {
		var out []col
		add := func(w int, cl col) {
			if w > 0 {
				cl.w = w
				out = append(out, cl)
			}
		}
		if g == nil {
			add(1, col{text: " "})
			add(c.where, col{text: "repositorio"})
			add(c.branch, col{text: "rama"})
			add(c.harness, col{text: "herramienta"})
			add(c.models, col{text: "modelos"})
			add(5, col{text: "pet.", right: true})
			add(9, col{text: "última"})
			add(c.who, col{text: "persona"})
			add(9, col{text: "coste", right: true})
			return out
		}
		check, cs := "○", s.Dim
		if m.selected[groupKey(*g)] {
			check, cs = "●", s.Hi
		}
		whereSt := s.Base
		if g.Bucket != api.BucketRepo {
			whereSt = s.Faint.Italic(true)
		}
		if sel {
			whereSt = whereSt.Bold(true)
		}
		branch := "—"
		if g.Branch != nil && *g.Branch != "" {
			branch = *g.Branch
		}
		add(1, col{text: check, st: cs})
		add(c.where, col{text: groupWhere(*g, m.sh.org), st: whereSt})
		add(c.branch, col{text: branch, st: s.Faint})
		add(c.harness, col{text: harnessName(string(g.Harness)), st: s.Faint})
		add(c.models, col{text: groupModels(*g), st: s.Faint})
		add(5, col{text: fmt.Sprint(g.Events), right: true, st: s.Faint})
		add(9, col{text: ago(g.LastAt, m.sh.now()), st: s.Faint})
		add(c.who, col{text: groupWho(*g), st: s.Faint})
		add(9, col{text: m.sh.money(g.CostUsdMicros), right: true, st: s.Base})
		return out
	}

	header := headerRow(s, width, cols(nil, false)...)
	list := m.list.View(width, func(g api.InboxGroup, sel bool, w int) string {
		return row(s, w, sel, cols(&g, sel)...)
	})
	if m.list.Len() == 0 {
		list = emptyState(s, width, max(1, height-inboxTop-inboxBottom), "Nada coincide con la búsqueda", "Pulsa esc para quitar el filtro.")
	}
	return toolbar + "\n" + intro + "\n\n" + header + "\n" + list + "\n" + m.selectionBar(width)
}

// selectionBar summarises what is marked and what enter will do.
func (m *inboxScreen) selectionBar(width int) string {
	s := m.sh.st
	n := len(m.selected)
	if n == 0 {
		return ""
	}
	left := s.Hi.Background(s.Surface).Render(" "+plural(n, "sesión marcada", "sesiones marcadas")) +
		s.Base.Background(s.Surface).Render("  "+m.sh.money(m.selectedCost()))
	right := s.Key.Background(s.Surface).Render("enter") + s.Faint.Background(s.Surface).Render(" asignar a una tarea   ") +
		s.Key.Background(s.Surface).Render("esc") + s.Faint.Background(s.Surface).Render(" desmarcar ")
	return spreadBg(s, left, right, width)
}

// spreadBg is spread() with the gap painted in the surface colour.
func spreadBg(s *theme.Styles, left, right string, w int) string {
	gap := max(1, w-lipgloss.Width(left)-lipgloss.Width(right))
	return left + s.Sel.Render(strings.Repeat(" ", gap)) + right
}
