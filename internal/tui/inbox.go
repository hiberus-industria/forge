package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/domain"
)

type inboxKeys struct{ Up, Down, Toggle, All, Assign, Mine, Refresh key.Binding }

func (k inboxKeys) ShortHelp() []key.Binding {
	return []key.Binding{k.Toggle, k.All, k.Assign, k.Mine, k.Refresh}
}

func (k inboxKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Up, k.Down, k.Toggle, k.All}, {k.Assign, k.Mine, k.Refresh}}
}

var inboxKeyMap = inboxKeys{
	Up:      key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "subir")),
	Down:    key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "bajar")),
	Toggle:  key.NewBinding(key.WithKeys("space", "x"), key.WithHelp("espacio", "marcar")),
	All:     key.NewBinding(key.WithKeys("A"), key.WithHelp("A", "marcar todo")),
	Assign:  key.NewBinding(key.WithKeys("enter", "m"), key.WithHelp("enter", "asignar a tarea")),
	Mine:    key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "mías/equipo")),
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

type inboxScreen struct {
	sh       *shared
	groups   []api.InboxGroup
	loading  bool
	err      error
	seq      int
	cursor   int
	selected map[string]bool
	all      bool
}

func newInboxScreen(sh *shared) *inboxScreen {
	return &inboxScreen{sh: sh, selected: map[string]bool{}, loading: true}
}

func (m *inboxScreen) Keys() help.KeyMap { return inboxKeyMap }
func (m *inboxScreen) Capturing() bool   { return false }
func (m *inboxScreen) Init() tea.Cmd     { return m.load() }

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

// selection returns the selected groups, or the one under the cursor when
// nothing is marked.
func (m *inboxScreen) selection() []api.InboxGroup {
	var out []api.InboxGroup
	for _, g := range m.groups {
		if m.selected[groupKey(g)] {
			out = append(out, g)
		}
	}
	if len(out) == 0 && m.cursor < len(m.groups) {
		out = append(out, m.groups[m.cursor])
	}
	return out
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

func (m *inboxScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case inboxLoadedMsg:
		if msg.seq != m.seq {
			return m, nil
		}
		m.loading, m.err = false, msg.err
		if msg.err == nil {
			m.groups = msg.groups
			keep := map[string]bool{}
			for _, g := range m.groups {
				if k := groupKey(g); m.selected[k] {
					keep[k] = true
				}
			}
			m.selected = keep
			m.cursor = min(m.cursor, max(0, len(m.groups)-1))
		}
	case refreshMsg:
		return m, m.load()
	case pickTasksMsg:
		return m, m.openPicker(msg)
	case reassignDoneMsg:
		m.selected = map[string]bool{}
		text := fmt.Sprintf("%d eventos asignados a %s", msg.res.Updated, msg.to)
		kind := toastOK
		if msg.res.Skipped > 0 {
			text += fmt.Sprintf(" · %d omitidos (no son tuyos)", msg.res.Skipped)
			kind = toastWarn
		}
		return m, tea.Batch(toast(kind, text), refresh)
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.cursor = max(0, m.cursor-1)
		case tea.MouseWheelDown:
			m.cursor = min(max(0, len(m.groups)-1), m.cursor+1)
		}
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, inboxKeyMap.Up):
			m.cursor = max(0, m.cursor-1)
		case key.Matches(msg, inboxKeyMap.Down):
			m.cursor = min(max(0, len(m.groups)-1), m.cursor+1)
		case key.Matches(msg, inboxKeyMap.Toggle):
			if m.cursor < len(m.groups) {
				k := groupKey(m.groups[m.cursor])
				m.selected[k] = !m.selected[k]
				if !m.selected[k] {
					delete(m.selected, k)
				}
				m.cursor = min(len(m.groups)-1, m.cursor+1)
			}
		case key.Matches(msg, inboxKeyMap.All):
			if len(m.selected) == len(m.groups) {
				m.selected = map[string]bool{}
			} else {
				for _, g := range m.groups {
					m.selected[groupKey(g)] = true
				}
			}
		case key.Matches(msg, inboxKeyMap.Mine):
			m.all = !m.all
			m.selected = map[string]bool{}
			m.cursor = 0
			return m, m.load()
		case key.Matches(msg, inboxKeyMap.Refresh):
			return m, m.load()
		case key.Matches(msg, inboxKeyMap.Assign):
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
	}
	return m, nil
}

func (m *inboxScreen) openPicker(msg pickTasksMsg) tea.Cmd {
	groups := m.selection()
	if len(groups) == 0 {
		return nil
	}
	sh := m.sh
	var items []pickItem
	// Suggest tasks of the same repository first.
	repoHint := ""
	if groups[0].Repo != nil {
		repoHint = *groups[0].Repo
	}
	for pass := 0; pass < 2; pass++ {
		for _, t := range msg.tasks {
			same := t.Ref.Repo == repoHint
			if (pass == 0) != same {
				continue
			}
			ref, err := issueRef(t.Ref)
			if err != nil {
				continue
			}
			items = append(items, pickItem{ref: ref, title: t.Title})
		}
	}
	req, cost := buildReassign(groups, nil, "")
	sub := fmt.Sprintf("%d grupos · %d eventos · %s", len(groups), len(req.EventIds), sh.money(cost))
	if msg.err != nil {
		sub += "\n" + lipgloss.NewStyle().Foreground(sh.st.Warning).Render("no se pudieron cargar tareas: escribe repo#número")
	}
	return openModal(newPickerModal(sh.st, "Asignar a tarea", sub, sh.org, items, func(ref domain.IssueRef) tea.Cmd {
		req, cost := buildReassign(groups, &ref, "triaje desde la bandeja")
		return openModal(&confirmModal{
			title: "Confirmar reasignación",
			body:  fmt.Sprintf("Mover %d eventos (%s) a %s.\nQuedará registrado en la auditoría.", len(req.EventIds), sh.money(cost), ref),
			onYes: func() tea.Msg {
				res, err := sh.be.Reassign(sh.ctx, req)
				if err != nil {
					return errMsg{err}
				}
				return reassignDoneMsg{res: res, to: ref}
			},
		})
	}))
}

func (m *inboxScreen) View(width, height int) string {
	s := m.sh.st
	scope := "mía"
	if m.all {
		scope = "de todo el equipo"
	}
	var total int64
	for _, g := range m.groups {
		total += g.CostUsdMicros
	}
	title := s.Title.Render("Bandeja") + s.Faint.Render(fmt.Sprintf(" · %s · %d sesiones sin asignar", scope, len(m.groups)))
	right := ""
	if n := len(m.selected); n > 0 {
		right = lipgloss.NewStyle().Foreground(s.Primary).Bold(true).Render(fmt.Sprintf("%d marcadas  ", n))
	}
	right += s.Faint.Render("pendiente ") + lipgloss.NewStyle().Foreground(s.Warning).Bold(true).Render(m.sh.money(total))
	head := spread(title, right, width)
	bodyH := height - 2

	switch {
	case m.loading && len(m.groups) == 0:
		return head + "\n" + center(s.Faint.Render("Cargando bandeja…"), width, bodyH)
	case m.err != nil && len(m.groups) == 0:
		return head + "\n" + center(lipgloss.NewStyle().Foreground(s.Danger).Render("✗ "+m.err.Error()), width, bodyH)
	case len(m.groups) == 0:
		msg := s.GradientText("Bandeja vacía", true) + "\n\n" + s.Faint.Render("Todo tu gasto está imputado a una tarea. ✨")
		return head + "\n" + center(msg, width, bodyH)
	}

	// Each group takes two lines.
	rows := max(1, bodyH/2)
	start, end := window(len(m.groups), m.cursor, rows)
	var b strings.Builder
	for i := start; i < end; i++ {
		b.WriteString(m.groupView(m.groups[i], i == m.cursor, width))
		if i < end-1 {
			b.WriteByte('\n')
		}
	}
	return head + "\n" + fitHeight(b.String(), bodyH)
}

func (m *inboxScreen) groupView(g api.InboxGroup, cur bool, width int) string {
	s := m.sh.st
	check := s.Dim.Render("○")
	if m.selected[groupKey(g)] {
		check = lipgloss.NewStyle().Foreground(s.Primary).Bold(true).Render("●")
	}
	where := lipgloss.NewStyle().Foreground(s.Warning).Render("personal / otros")
	if g.Bucket == api.BucketRepo && g.Repo != nil {
		where = lipgloss.NewStyle().Foreground(s.Secondary).Bold(true).Render(shortRepo(*g.Repo, m.sh.org))
		if g.Branch != nil && *g.Branch != "" {
			where += s.Faint.Render(" ⎇ " + *g.Branch)
		}
	}
	cost := lipgloss.NewStyle().Bold(true).Render(m.sh.money(g.CostUsdMicros))
	l1 := spread(check+" "+harnessIcon(s, string(g.Harness))+" "+where, cost, width-2)
	models := ""
	if g.Models != nil {
		ms := make([]string, len(*g.Models))
		for i, x := range *g.Models {
			ms[i] = shortModel(x)
		}
		models = strings.Join(ms, ", ")
	}
	who := g.User.Name
	if g.User.GithubLogin != nil {
		who = "@" + *g.User.GithubLogin
	}
	l2 := s.Faint.Render(fmt.Sprintf("    %s · %d peticiones · %s · %s · %s", harnessName(string(g.Harness)), g.Events, models, ago(g.LastAt, m.sh.now()), who))
	l2 = trunc(l2, width-2)
	if cur {
		mark := lipgloss.NewStyle().Foreground(s.Primary).Bold(true).Render("❯ ")
		return mark + s.Selected.Width(width-2).Render(l1) + "\n  " + s.Selected.Width(width-2).Render(l2)
	}
	return "  " + l1 + "\n  " + l2
}
