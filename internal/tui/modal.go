package tui

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

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
	View(s *theme.Styles, width int) string
}

func modalWidth(width int) int { return max(36, min(72, width-8)) }

func modalFrame(s *theme.Styles, width int, title, body, footer string) string {
	inner := modalWidth(width) - 6
	var b strings.Builder
	b.WriteString(s.GradientText(title, true))
	b.WriteString("\n\n")
	b.WriteString(body)
	if footer != "" {
		b.WriteString("\n\n")
		b.WriteString(s.Faint.Render(footer))
	}
	return s.Modal.Width(inner + 6).Render(b.String())
}

// ---------------------------------------------------------------- choice

type choice struct{ label, hint string }

type choiceModal struct {
	title, subtitle string
	opts            []choice
	cursor          int
	onPick          func(i int) tea.Cmd
}

func (m *choiceModal) Update(msg tea.Msg) (modal, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "up", "k", "shift+tab":
			m.cursor = (m.cursor - 1 + len(m.opts)) % len(m.opts)
		case "down", "j", "tab":
			m.cursor = (m.cursor + 1) % len(m.opts)
		case "enter", "space":
			return m, tea.Sequence(closeModal, m.onPick(m.cursor))
		case "esc", "q":
			return m, closeModal
		default:
			// Quick select with the option number.
			if r := []rune(k.String()); len(r) == 1 && r[0] >= '1' && int(r[0]-'1') < len(m.opts) {
				m.cursor = int(r[0] - '1')
				return m, tea.Sequence(closeModal, m.onPick(m.cursor))
			}
		}
	}
	return m, nil
}

func (m *choiceModal) View(s *theme.Styles, width int) string {
	inner := modalWidth(width) - 6
	labelW := 0
	for _, o := range m.opts {
		labelW = max(labelW, lipgloss.Width(o.label))
	}
	var b strings.Builder
	if m.subtitle != "" {
		b.WriteString(s.Base.Width(inner).Render(m.subtitle) + "\n\n")
	}
	for i, o := range m.opts {
		num := s.Faint.Render(fmt.Sprintf("%d ", i+1))
		label, mark := pad(o.label, labelW), "  "
		if i == m.cursor {
			label = lipgloss.NewStyle().Bold(true).Foreground(s.Primary).Render(label)
			mark = lipgloss.NewStyle().Foreground(s.Primary).Bold(true).Render("❯ ")
		}
		line := mark + num + label
		if o.hint != "" {
			line += "  " + s.Faint.Render(o.hint)
		}
		b.WriteString(trunc(line, inner) + "\n")
	}
	return modalFrame(s, width, m.title, strings.TrimRight(b.String(), "\n"), "↑/↓ elegir · enter confirmar · esc cancelar")
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

func (m *confirmModal) View(s *theme.Styles, width int) string {
	body := s.Base.Width(modalWidth(width) - 6).Render(m.body)
	return modalFrame(s, width, m.title, body, "enter/s confirmar · esc/n cancelar")
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
	ti.Prompt = "❯ "
	ti.SetStyles(inputStyles(s))
	ti.SetWidth(48)
	ti.Focus()
	return &inputModal{title: title, hint: hint, input: ti, validate: validate, onSubmit: onSubmit}
}

func inputStyles(s *theme.Styles) textinput.Styles {
	st := textinput.DefaultStyles(s.IsDark)
	st.Focused.Prompt = lipgloss.NewStyle().Foreground(s.Primary).Bold(true)
	st.Blurred.Prompt = s.Faint
	st.Focused.Placeholder = s.Dim
	st.Blurred.Placeholder = s.Dim
	return st
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

func (m *inputModal) View(s *theme.Styles, width int) string {
	body := s.Faint.Render(m.hint) + "\n\n" + m.input.View()
	if m.err != "" {
		body += "\n" + lipgloss.NewStyle().Foreground(s.Danger).Render("✗ "+m.err)
	}
	return modalFrame(s, width, m.title, body, "enter aceptar · esc cancelar")
}

// ---------------------------------------------------------------- issue picker

type pickItem struct {
	ref   domain.IssueRef
	title string
}

// pickerModal lets the user fuzzy-find an issue or type owner/repo#n.
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
	ti.Prompt = "⌕ "
	ti.SetStyles(inputStyles(s))
	ti.SetWidth(50)
	ti.Focus()
	return &pickerModal{title: title, subtitle: subtitle, org: org, items: items, input: ti, onPick: onPick}
}

// parseLooseRef accepts owner/repo#n, repo#n (tracked org) and #n is rejected.
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
			m.err = "elige una tarea o escribe repo#número"
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

func (m *pickerModal) View(s *theme.Styles, width int) string {
	inner := modalWidth(width) - 6
	var b strings.Builder
	if m.subtitle != "" {
		b.WriteString(s.Base.Render(m.subtitle) + "\n\n")
	}
	b.WriteString(m.input.View() + "\n\n")
	items := m.filtered()
	start, end := window(len(items), m.cursor, 8)
	if len(items) == 0 {
		if _, err := parseLooseRef(m.input.Value(), m.org); err == nil {
			b.WriteString(s.Faint.Render("enter para usar la referencia escrita"))
		} else {
			b.WriteString(s.Faint.Render("sin coincidencias"))
		}
	}
	for i := start; i < end; i++ {
		it := items[i]
		ref := lipgloss.NewStyle().Foreground(s.Secondary).Render(fmt.Sprintf("%s#%d", shortRepo(it.ref.Repo.String(), m.org), it.ref.Number))
		line := ref + " " + it.title
		if i == m.cursor {
			b.WriteString(lipgloss.NewStyle().Foreground(s.Primary).Render("❯ ") + s.Bold.Render(trunc(line, inner-2)))
		} else {
			b.WriteString("  " + trunc(line, inner-2))
		}
		if i < end-1 {
			b.WriteByte('\n')
		}
	}
	if m.err != "" {
		b.WriteString("\n" + lipgloss.NewStyle().Foreground(s.Danger).Render("✗ "+m.err))
	}
	return modalFrame(s, width, m.title, b.String(), "↑/↓ moverse · enter elegir · esc cancelar")
}

// ---------------------------------------------------------------- flow (progress)

type flowStepMsg struct {
	id   int
	text string
}

type flowEndMsg struct {
	id     int
	result string
	err    error
}

var flowSeq int

// runFlow runs fn in the background, streaming its progress as messages.
func runFlow(fn func(progress func(string)) (string, error)) (int, chan tea.Msg, tea.Cmd) {
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
	result string
	err    error
}

func newFlowModal(s *theme.Styles, title string, fn func(progress func(string)) (string, error)) (*flowModal, tea.Cmd) {
	id, ch, wait := runFlow(fn)
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(lipgloss.NewStyle().Foreground(s.Primary)))
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
		if m.done && (msg.String() == "enter" || msg.String() == "esc" || msg.String() == "q") {
			return m, closeModal
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

func (m *flowModal) View(s *theme.Styles, width int) string {
	ok := lipgloss.NewStyle().Foreground(s.Success).Render("✓")
	var b strings.Builder
	for i, st := range m.steps {
		mark := ok
		if i == len(m.steps)-1 && !m.done {
			mark = m.spin.View()
		}
		b.WriteString(mark + " " + st + "\n")
	}
	if len(m.steps) == 0 && !m.done {
		b.WriteString(m.spin.View() + " " + s.Faint.Render("preparando…") + "\n")
	}
	footer := "trabajando…"
	if m.done {
		footer = "enter/esc cerrar"
		if m.err != nil {
			b.WriteString("\n" + lipgloss.NewStyle().Foreground(s.Danger).Bold(true).Render("✗ "+m.err.Error()))
		} else {
			b.WriteString("\n" + lipgloss.NewStyle().Foreground(s.Success).Bold(true).Render("✔ "+m.result))
		}
	}
	return modalFrame(s, width, m.title, strings.TrimRight(b.String(), "\n"), footer)
}
