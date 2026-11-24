package tui

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/hiberus-industria/forge/internal/domain"
	"github.com/hiberus-industria/forge/internal/tui/theme"
)

// modal is a dialog shown over the active screen. It gets every key while
// open, plus every non-key message.
type modal interface {
	Update(msg tea.Msg) (modal, tea.Cmd)
	View(s *theme.Styles, width, height int) string
}

// modalInner is the content width of a modal for a terminal width.
func modalInner(width int) int { return max(30, min(66, width-12)) }

// modalFrame draws every dialog the same way: title, optional subtitle,
// body and a footer of key hints.
func modalFrame(s *theme.Styles, width int, title, subtitle, body string, keys ...string) string {
	return modalFrameInner(s, modalInner(width), title, subtitle, body, keys...)
}

func modalFrameInner(s *theme.Styles, inner int, title, subtitle, body string, keys ...string) string {
	parts := []string{s.Bold.Render(trunc(title, inner))}
	if subtitle != "" {
		parts = append(parts, s.Faint.Width(inner).Render(subtitle))
	}
	parts = append(parts, "", body)
	if len(keys) > 0 {
		var hs []string
		for i := 0; i+1 < len(keys); i += 2 {
			hs = append(hs, hint(s, keys[i], keys[i+1]))
		}
		parts = append(parts, "", strings.Join(hs, "   "))
	}
	return s.Modal.Width(inner + 6).Render(strings.Join(parts, "\n"))
}

// ---------------------------------------------------------------- choice

// choice is one option of a menu. key is its shortcut inside the menu (and,
// for action menus, the same shortcut that works from the screen).
type choice struct {
	key, label, hint string
	disabled         string // reason; empty when enabled
}

type choiceModal struct {
	title, subtitle string
	opts            []choice
	cursor          int
	onPick          func(i int) tea.Cmd
}

func (m *choiceModal) move(d int) {
	for range m.opts {
		m.cursor = (m.cursor + d + len(m.opts)) % len(m.opts)
		if m.opts[m.cursor].disabled == "" {
			return
		}
	}
}

func (m *choiceModal) pick(i int) (modal, tea.Cmd) {
	if m.opts[i].disabled != "" {
		return m, toast(toastWarn, m.opts[i].disabled)
	}
	return m, tea.Sequence(closeModal, m.onPick(i))
}

func (m *choiceModal) Update(msg tea.Msg) (modal, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "up", "k", "shift+tab":
		m.move(-1)
	case "down", "j", "tab":
		m.move(1)
	case "enter", "space":
		return m.pick(m.cursor)
	case "esc", "q":
		return m, closeModal
	default:
		for i, o := range m.opts {
			if o.key != "" && o.key == k.String() {
				return m.pick(i)
			}
		}
		if r := []rune(k.String()); len(r) == 1 && r[0] >= '1' && int(r[0]-'1') < len(m.opts) {
			return m.pick(int(r[0] - '1'))
		}
	}
	return m, nil
}

func (m *choiceModal) View(s *theme.Styles, width, _ int) string {
	inner := modalInner(width)
	labelW := 0
	for _, o := range m.opts {
		labelW = max(labelW, lipgloss.Width(o.label))
	}
	lines := make([]string, len(m.opts))
	for i, o := range m.opts {
		k := o.key
		if k == "" {
			k = fmt.Sprint(i + 1)
		}
		sel := i == m.cursor
		gutter, label, ks := "  ", s.Base, s.Faint
		switch {
		case o.disabled != "":
			label, ks = s.Dim, s.Dim
		case sel:
			gutter, label, ks = s.Cursor.Render("▌")+" ", s.Bold, s.Hi
		}
		line := gutter + ks.Render(pad(k, 2)) + " " + label.Render(pad(o.label, labelW))
		if note := o.hint; o.disabled != "" {
			line += "  " + s.Dim.Render(o.disabled)
		} else if note != "" {
			line += "  " + s.Faint.Render(note)
		}
		lines[i] = trunc(line, inner)
	}
	return modalFrame(s, width, m.title, m.subtitle, strings.Join(lines, "\n"), "↑↓", "elegir", "enter", "aceptar", "esc", "cerrar")
}

// ---------------------------------------------------------------- confirm

type confirmModal struct {
	title, body string
	onYes       tea.Cmd
}

func (m *confirmModal) Update(msg tea.Msg) (modal, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "y", "s", "enter":
			return m, tea.Sequence(closeModal, m.onYes)
		case "n", "esc", "q":
			return m, closeModal
		}
	}
	return m, nil
}

func (m *confirmModal) View(s *theme.Styles, width, _ int) string {
	body := s.Base.Width(modalInner(width)).Render(m.body)
	return modalFrame(s, width, m.title, "", body, "enter", "sí", "esc", "no")
}

// ---------------------------------------------------------------- input

type inputModal struct {
	title, hint string
	input       textinput.Model
	err         string
	validate    func(string) error
	onSubmit    func(string) tea.Cmd
}

func newInputModal(s *theme.Styles, title, hint, placeholder string, validate func(string) error, onSubmit func(string) tea.Cmd) *inputModal {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.Prompt = "› "
	ti.SetStyles(inputStyles(s))
	ti.SetWidth(40)
	ti.Focus()
	return &inputModal{title: title, hint: hint, input: ti, validate: validate, onSubmit: onSubmit}
}

func (m *inputModal) Update(msg tea.Msg) (modal, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc":
			return m, closeModal
		case "enter":
			v := strings.TrimSpace(m.input.Value())
			if m.validate != nil {
				if err := m.validate(v); err != nil {
					m.err = err.Error()
					return m, nil
				}
			}
			return m, tea.Sequence(closeModal, m.onSubmit(v))
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.err = ""
	return m, cmd
}

func (m *inputModal) View(s *theme.Styles, width, _ int) string {
	m.input.SetWidth(modalInner(width) - 4)
	body := m.input.View()
	if m.err != "" {
		body += "\n" + s.Err.Render(m.err)
	}
	return modalFrame(s, width, m.title, m.hint, body, "enter", "aplicar", "esc", "cancelar")
}

// ---------------------------------------------------------------- issue picker

type pickItem struct {
	ref       domain.IssueRef
	title     string
	suggested bool // same repository as what is being assigned
}

// pickerModal lets the user find an issue by title or type owner/repo#n.
type pickerModal struct {
	title, subtitle string
	org             string
	items           []pickItem
	input           textinput.Model
	cursor          int
	err             string
	onPick          func(domain.IssueRef) tea.Cmd
}

func newPickerModal(s *theme.Styles, title, subtitle, org string, items []pickItem, onPick func(domain.IssueRef) tea.Cmd) *pickerModal {
	ti := textinput.New()
	ti.Placeholder = "busca por título o escribe repo#123"
	ti.Prompt = "/ "
	ti.SetStyles(inputStyles(s))
	ti.Focus()
	return &pickerModal{title: title, subtitle: subtitle, org: org, items: items, input: ti, onPick: onPick}
}

// parseLooseRef accepts owner/repo#n and repo#n (tracked org); #n is rejected.
func parseLooseRef(s, org string) (domain.IssueRef, error) {
	s = strings.TrimSpace(s)
	if repo, _, ok := strings.Cut(s, "#"); ok && !strings.Contains(repo, "/") && repo != "" {
		s = org + "/" + s
	}
	return domain.ParseIssueRef(s)
}

// fuzzyScore returns a subsequence match score (higher is better), -1 if no match.
func fuzzyScore(query, target string) int {
	q := []rune(strings.ToLower(query))
	if len(q) == 0 {
		return 0
	}
	t := []rune(strings.ToLower(target))
	score, qi, streak := 0, 0, 0
	for i := 0; i < len(t) && qi < len(q); i++ {
		if t[i] == q[qi] {
			qi++
			streak++
			score += 1 + streak*2
			if i == 0 || !unicode.IsLetter(t[i-1]) {
				score += 3
			}
		} else {
			streak = 0
		}
	}
	if qi < len(q) {
		return -1
	}
	return score
}

func (m *pickerModal) filtered() []pickItem {
	q := strings.TrimSpace(m.input.Value())
	if q == "" {
		return m.items
	}
	type scored struct {
		it pickItem
		s  int
	}
	var out []scored
	for _, it := range m.items {
		if sc := fuzzyScore(q, it.ref.String()+" "+it.title); sc >= 0 {
			out = append(out, scored{it, sc})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].s > out[j].s })
	res := make([]pickItem, len(out))
	for i, o := range out {
		res[i] = o.it
	}
	return res
}

func (m *pickerModal) Update(msg tea.Msg) (modal, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		items := m.filtered()
		switch k.String() {
		case "esc":
			return m, closeModal
		case "up", "ctrl+p":
			m.cursor = max(0, m.cursor-1)
			return m, nil
		case "down", "ctrl+n":
			m.cursor = min(max(0, len(items)-1), m.cursor+1)
			return m, nil
		case "enter":
			if ref, err := parseLooseRef(m.input.Value(), m.org); err == nil {
				return m, tea.Sequence(closeModal, m.onPick(ref))
			}
			if m.cursor < len(items) {
				return m, tea.Sequence(closeModal, m.onPick(items[m.cursor].ref))
			}
			m.err = "Elige una tarea de la lista o escribe repo#número"
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if _, ok := msg.(tea.KeyPressMsg); ok {
		m.err = ""
		m.cursor = min(m.cursor, max(0, len(m.filtered())-1))
	}
	return m, cmd
}

func (m *pickerModal) View(s *theme.Styles, width, height int) string {
	inner := modalInner(width)
	m.input.SetWidth(inner - 4)
	lines := []string{m.input.View(), ""}
	items := m.filtered()
	rows := max(3, min(10, height-16))
	start, end := window(len(items), m.cursor, rows)
	if len(items) == 0 {
		if _, err := parseLooseRef(m.input.Value(), m.org); err == nil {
			lines = append(lines, s.Faint.Render("Pulsa enter para usar esa referencia"))
		} else {
			lines = append(lines, s.Faint.Render("Ninguna tarea abierta coincide"))
		}
	}
	for i := start; i < end; i++ {
		it := items[i]
		ref := fmt.Sprintf("%s#%d", shortRepo(it.ref.Repo.String(), m.org), it.ref.Number)
		gutter, ts := "  ", s.Base
		if i == m.cursor {
			gutter, ts = s.Cursor.Render("▌")+" ", s.Bold
		}
		lines = append(lines, trunc(gutter+s.Faint.Render(pad(ref, 10))+" "+ts.Render(it.title), inner))
	}
	if n := len(items); n > rows {
		lines = append(lines, s.Dim.Render(fmt.Sprintf("  %d de %d", m.cursor+1, n)))
	}
	if m.err != "" {
		lines = append(lines, "", s.Err.Render(m.err))
	}
	return modalFrame(s, width, m.title, m.subtitle, strings.Join(lines, "\n"), "↑↓", "elegir", "enter", "asignar", "esc", "cancelar")
}

// ---------------------------------------------------------------- flow (progress)

type flowStepMsg struct {
	id   int
	text string
}

// flowResult is what a finished flow reports: a sentence and, optionally,
// the URL of what it created.
type flowResult struct {
	text, url, urlLabel string
}

type flowEndMsg struct {
	id     int
	result flowResult
	err    error
}

var flowSeq int

// runFlow runs fn in the background, streaming its progress as messages.
func runFlow(fn func(progress func(string)) (flowResult, error)) (int, chan tea.Msg, tea.Cmd) {
	flowSeq++
	id := flowSeq
	ch := make(chan tea.Msg, 64)
	go func() {
		res, err := fn(func(step string) {
			select {
			case ch <- flowStepMsg{id: id, text: step}:
			default:
			}
		})
		ch <- flowEndMsg{id: id, result: res, err: err}
		close(ch)
	}()
	return id, ch, waitFlow(ch)
}

func waitFlow(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		m, ok := <-ch
		if !ok {
			return nil
		}
		return m
	}
}

type flowModal struct {
	id     int
	ch     chan tea.Msg
	title  string
	steps  []string
	spin   spinner.Model
	done   bool
	result flowResult
	err    error
}

func newFlowModal(s *theme.Styles, title string, fn func(progress func(string)) (flowResult, error)) (*flowModal, tea.Cmd) {
	id, ch, wait := runFlow(fn)
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(s.Hi))
	m := &flowModal{id: id, ch: ch, title: title, spin: sp}
	return m, tea.Batch(wait, m.spin.Tick)
}

func (m *flowModal) Update(msg tea.Msg) (modal, tea.Cmd) {
	switch msg := msg.(type) {
	case flowStepMsg:
		if msg.id == m.id {
			m.steps = append(m.steps, msg.text)
			return m, waitFlow(m.ch)
		}
	case flowEndMsg:
		if msg.id == m.id {
			m.done, m.result, m.err = true, msg.result, msg.err
			return m, refresh
		}
	case tea.KeyPressMsg:
		if !m.done {
			return m, nil
		}
		switch msg.String() {
		case "enter", "esc", "q":
			return m, closeModal
		case "o":
			if m.result.url != "" {
				return m, openURL(m.result.url)
			}
		case "y":
			if m.result.url != "" {
				return m, copyText(m.result.url, "Enlace")
			}
		}
	case spinner.TickMsg:
		if !m.done {
			var cmd tea.Cmd
			m.spin, cmd = m.spin.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m *flowModal) View(s *theme.Styles, width, _ int) string {
	inner := modalInner(width)
	var lines []string
	for i, st := range m.steps {
		mark, text := s.OK.Render("✓"), s.Faint.Render(st)
		if i == len(m.steps)-1 && !m.done {
			mark, text = m.spin.View(), s.Base.Render(st)
		}
		lines = append(lines, trunc(mark+" "+text, inner))
	}
	if len(m.steps) == 0 && !m.done {
		lines = append(lines, m.spin.View()+" "+s.Faint.Render("Preparando…"))
	}
	if !m.done {
		return modalFrame(s, width, m.title, "", strings.Join(lines, "\n"))
	}
	if m.err != nil {
		lines = append(lines, "", s.Err.Width(inner).Render(m.err.Error()))
		return modalFrame(s, width, m.title, "", strings.Join(lines, "\n"), "enter", "cerrar")
	}
	lines = append(lines, "", s.Bold.Width(inner).Render(m.result.text))
	if m.result.url != "" {
		label := m.result.urlLabel
		if label == "" {
			label = m.result.url
		}
		lines = append(lines, link(s, m.result.url, trunc(label, inner)))
		return modalFrame(s, width, m.title, "", strings.Join(lines, "\n"), "o", "abrir", "y", "copiar enlace", "enter", "cerrar")
	}
	return modalFrame(s, width, m.title, "", strings.Join(lines, "\n"), "enter", "cerrar")
}

// ---------------------------------------------------------------- help

// helpModal lists every binding of the active screen plus the global ones,
// in as many columns as fit, never taller than the terminal.
type helpModal struct {
	title  string
	groups []helpGroup
	offset int
}

func (m *helpModal) Update(msg tea.Msg) (modal, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "?", "esc", "q", "enter":
			return m, closeModal
		case "down", "j":
			m.offset++
		case "up", "k":
			m.offset = max(0, m.offset-1)
		}
	}
	return m, nil
}

func (m *helpModal) View(s *theme.Styles, width, height int) string {
	blocks := make([]string, len(m.groups))
	colW := 0
	for i, g := range m.groups {
		keyW := 0
		for _, b := range g.keys {
			keyW = max(keyW, lipgloss.Width(b.Help().Key))
		}
		lines := []string{s.Section.Render(g.title)}
		for _, b := range g.keys {
			if !b.Enabled() {
				continue
			}
			lines = append(lines, s.Key.Render(pad(b.Help().Key, keyW))+"  "+s.Faint.Render(b.Help().Desc))
		}
		blocks[i] = strings.Join(lines, "\n")
		colW = max(colW, lipgloss.Width(blocks[i]))
	}
	inner := min(width-8, 96)
	cols := max(1, min(len(blocks), (inner+4)/(colW+4)))
	var rows []string
	for i := 0; i < len(blocks); i += cols {
		var cells []string
		for j := i; j < min(i+cols, len(blocks)); j++ {
			st := lipgloss.NewStyle().Width(colW)
			if j > i {
				st = st.PaddingLeft(4).Width(colW + 4)
			}
			cell := st.Render(blocks[j])
			cells = append(cells, cell)
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, cells...))
	}
	body := strings.Join(rows, "\n\n")
	// Never taller than the screen (the frame takes 8 lines): scroll instead.
	lines := strings.Split(body, "\n")
	room := max(3, height-8)
	keys := []string{"esc", "cerrar"}
	if len(lines) > room {
		m.offset = min(m.offset, len(lines)-room)
		lines = lines[m.offset : m.offset+room]
		keys = append([]string{"↑↓", "desplazar"}, keys...)
	}
	body = strings.Join(lines, "\n")
	return modalFrameInner(s, max(30, lipgloss.Width(body)), m.title, "", body, keys...)
}

// globalHelp are the bindings available on every tab.
func globalHelp() helpGroup {
	return helpGroup{"General", []key.Binding{globalKeyMap.Tabs, globalKeyMap.Next, globalKeyMap.Help, globalKeyMap.Quit}}
}
