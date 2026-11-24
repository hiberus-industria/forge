package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/domain"
	"github.com/hiberus-industria/forge/internal/tui/theme"
)

const logo = `█▀▀ █▀█ █▀█ █▀▀ █▀▀
█▀  █▄█ █▀▄ █▄█ ██▄`

type globalKeys struct{ Tabs, Next, Help, Quit key.Binding }

var globalKeyMap = globalKeys{
	Tabs: key.NewBinding(key.WithKeys("1", "2", "3", "4"), key.WithHelp("1-4", "ir a una pestaña")),
	Next: key.NewBinding(key.WithKeys("tab", "shift+tab"), key.WithHelp("tab", "pestaña siguiente")),
	Help: key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "ayuda")),
	Quit: key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "salir")),
}

type toastItem struct {
	id   int
	text string
	kind toastKind
}

type model struct {
	sh      *shared
	version string

	screens []screen
	titles  []string
	active  int

	modal    modal
	toasts   []toastItem
	toastSeq int

	splash     bool
	splashSpin spinner.Model

	bodyW, bodyH int
}

func newModel(sh *shared, version string) *model {
	m := &model{
		sh:      sh,
		version: version,
		titles:  []string{"Tareas", "En vivo", "Bandeja", "Informes"},
		splash:  true,
	}
	m.screens = []screen{newTasksScreen(sh), newLiveScreen(sh), newInboxScreen(sh), newReportsScreen(sh)}
	m.splashSpin = spinner.New(spinner.WithSpinner(spinner.MiniDot))
	m.restyle()
	return m
}

// restyle propagates a theme change (dark/light) to the components.
func (m *model) restyle() {
	st := m.sh.st
	currentStyles = st
	m.splashSpin.Style = st.Hi
	for _, sc := range m.screens {
		if r, ok := sc.(interface{ restyle(*theme.Styles) }); ok {
			r.restyle(st)
		}
	}
}

// Layout: header, rule, [banner], body, status line, hints.
const bodyLeft = 2

func (m *model) bodyTop() int {
	if m.bannerView(m.sh.width) != "" {
		return 3
	}
	return 2
}

// layout gives the remaining area to every screen.
func (m *model) layout() {
	w, h := m.sh.width, m.sh.height
	if w == 0 || h == 0 {
		return
	}
	bw, bh := max(10, w-2*bodyLeft), max(3, h-m.bodyTop()-2)
	if bw == m.bodyW && bh == m.bodyH {
		return
	}
	m.bodyW, m.bodyH = bw, bh
	for _, sc := range m.screens {
		sc.SetSize(bw, bh)
	}
}

func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		tea.RequestBackgroundColor,
		statusNow(m.sh),
		m.statusTick(),
		m.fetchFX(),
		m.splashSpin.Tick,
		tea.Tick(600*time.Millisecond, func(time.Time) tea.Msg { return splashDoneMsg{} }),
	}
	for _, s := range m.screens {
		cmds = append(cmds, s.Init())
	}
	return tea.Batch(cmds...)
}

func (m *model) statusTick() tea.Cmd {
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return statusTickMsg{} })
}

// fetchFX derives the USD/EUR rate the API uses (ECB) from a small EUR report.
func (m *model) fetchFX() tea.Cmd {
	be, ctx, now := m.sh.be, m.sh.ctx, m.sh.now()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		eur := api.EUR
		from, to := now.AddDate(0, 0, -30), now.Add(time.Hour)
		r, err := be.Report(ctx, api.GetCostReportParams{GroupBy: api.GroupByDay, Currency: &eur, From: &from, To: &to})
		if err != nil || r.Total <= 0 || r.TotalUsdMicros <= 0 {
			return nil
		}
		return fxMsg{usdPerEUR: domain.Micros(r.TotalUsdMicros).USD() / r.Total}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.update(msg)
	m.layout()
	return m, cmd
}

func (m *model) update(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.sh.width, m.sh.height = msg.Width, msg.Height
		return nil
	case tea.BackgroundColorMsg:
		m.sh.st = m.sh.theme.Styles(msg.IsDark())
		m.restyle()
		return nil
	case statusMsg:
		m.splash = false
		if msg.err == nil {
			st := msg.st
			m.sh.status, m.sh.statusErr = &st, nil
		} else {
			m.sh.statusErr = msg.err
		}
		return nil
	case statusTickMsg:
		return tea.Batch(statusNow(m.sh), m.statusTick())
	case fxMsg:
		m.sh.usdPerEUR = msg.usdPerEUR
		return nil
	case splashDoneMsg:
		m.splash = false
		return nil
	case spinner.TickMsg:
		if m.splash && msg.ID == m.splashSpin.ID() {
			var cmd tea.Cmd
			m.splashSpin, cmd = m.splashSpin.Update(msg)
			return cmd
		}
	case toastMsg:
		m.toastSeq++
		id := m.toastSeq
		m.toasts = append(m.toasts, toastItem{id: id, text: msg.text, kind: msg.kind})
		if len(m.toasts) > 4 {
			m.toasts = m.toasts[len(m.toasts)-4:]
		}
		ttl := 4 * time.Second
		if msg.kind == toastErr {
			ttl = 8 * time.Second
		}
		return tea.Tick(ttl, func(time.Time) tea.Msg { return toastExpireMsg{id} })
	case toastExpireMsg:
		for i, t := range m.toasts {
			if t.id == msg.id {
				m.toasts = append(m.toasts[:i], m.toasts[i+1:]...)
				break
			}
		}
		return nil
	case errMsg:
		if classify(msg.err) == errDaemonDown {
			m.sh.statusErr = msg.err
		}
		return toast(toastErr, msg.err.Error())
	case openModalMsg:
		m.modal = msg.m
		return nil
	case closeModalMsg:
		m.modal = nil
		return nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.MouseClickMsg:
		if m.modal != nil {
			return nil
		}
		if msg.Y == 0 {
			if i := m.tabAt(msg.X); i >= 0 {
				m.active = i
			}
			return nil
		}
		return m.toScreen(msg.Mouse())
	case tea.MouseWheelMsg:
		if m.modal != nil {
			return nil
		}
		mm := msg.Mouse()
		mm.X, mm.Y = mm.X-bodyLeft, mm.Y-m.bodyTop()
		var cmd tea.Cmd
		m.screens[m.active], cmd = m.screens[m.active].Update(tea.MouseWheelMsg(mm))
		return cmd
	case tea.MouseMsg:
		return nil
	}

	// Everything else goes to every screen and to the open modal.
	for i, s := range m.screens {
		var cmd tea.Cmd
		m.screens[i], cmd = s.Update(msg)
		cmds = append(cmds, cmd)
	}
	if m.modal != nil {
		var cmd tea.Cmd
		m.modal, cmd = m.modal.Update(msg)
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

// toScreen forwards a click to the active screen in body coordinates.
func (m *model) toScreen(mm tea.Mouse) tea.Cmd {
	mm.X, mm.Y = mm.X-bodyLeft, mm.Y-m.bodyTop()
	if mm.Y < 0 || mm.Y >= m.bodyH {
		return nil
	}
	var cmd tea.Cmd
	m.screens[m.active], cmd = m.screens[m.active].Update(tea.MouseClickMsg(mm))
	return cmd
}

func (m *model) handleKey(k tea.KeyPressMsg) tea.Cmd {
	if k.String() == "ctrl+c" {
		return tea.Quit
	}
	if m.modal != nil {
		var cmd tea.Cmd
		m.modal, cmd = m.modal.Update(k)
		return cmd
	}
	scr := m.screens[m.active]
	if scr.Capturing() {
		var cmd tea.Cmd
		m.screens[m.active], cmd = scr.Update(k)
		return cmd
	}
	m.splash = false
	switch k.String() {
	case "q":
		return tea.Quit
	case "?":
		groups := append(scr.Help(), globalHelp())
		return openModal(&helpModal{title: "Atajos · " + m.titles[m.active], groups: groups})
	case "1", "2", "3", "4":
		m.active = int(k.String()[0] - '1')
		return nil
	case "tab":
		m.active = (m.active + 1) % len(m.screens)
		return nil
	case "shift+tab":
		m.active = (m.active - 1 + len(m.screens)) % len(m.screens)
		return nil
	}
	var cmd tea.Cmd
	m.screens[m.active], cmd = scr.Update(k)
	return cmd
}

// ---------------------------------------------------------------- view

func (m *model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "forge · " + m.titles[m.active]
	return v
}

func (m *model) render() string {
	w, h := m.sh.width, m.sh.height
	if w == 0 || h == 0 {
		return ""
	}
	if m.splash {
		return m.splashView(w, h)
	}
	s := m.sh.st

	parts := []string{m.headerView(w), s.Rule.Render(strings.Repeat("─", w))}
	if banner := m.bannerView(w); banner != "" {
		parts = append(parts, banner)
	}
	body := fitHeight(m.screens[m.active].View(), m.bodyH)
	margin := strings.Repeat(" ", bodyLeft)
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		lines[i] = margin + l
	}
	parts = append(parts, strings.Join(lines, "\n"), m.statusLine(w), m.footerView(w))
	base := fitHeight(strings.Join(parts, "\n"), h)

	if m.modal == nil {
		return base
	}
	mv := m.modal.View(s, w, h)
	mw, mh := lipgloss.Width(mv), lipgloss.Height(mv)
	return fitHeight(lipgloss.NewCompositor(
		lipgloss.NewLayer(base),
		lipgloss.NewLayer(mv).X(max(0, (w-mw)/2)).Y(max(0, (h-mh)/3)).Z(1),
	).Render(), h)
}

const brand = "forge"

// tabLabels returns the rendered tabs and their x ranges (for mouse clicks).
func (m *model) tabLabels() ([]string, [][2]int) {
	s := m.sh.st
	x := 1 + lipgloss.Width(brand) + 2
	var out []string
	var ranges [][2]int
	for i, t := range m.titles {
		label := t
		if b, ok := m.screens[i].(badged); ok && b.Badge() > 0 {
			label += fmt.Sprintf(" %d", b.Badge())
		}
		r := s.TabOff.Render(label)
		if i == m.active {
			r = s.TabOn.Render(label)
		}
		out = append(out, r)
		w := lipgloss.Width(r)
		ranges = append(ranges, [2]int{x, x + w})
		x += w
	}
	return out, ranges
}

func (m *model) tabAt(x int) int {
	_, ranges := m.tabLabels()
	for i, r := range ranges {
		if x >= r[0] && x < r[1] {
			return i
		}
	}
	return -1
}

func (m *model) headerView(w int) string {
	s := m.sh.st
	tabs, _ := m.tabLabels()
	left := " " + s.Hi.Render(brand) + "  " + strings.Join(tabs, "")

	var chips []string
	st := m.sh.status
	switch {
	case m.sh.statusErr != nil && classify(m.sh.statusErr) == errDaemonDown:
		chips = append(chips, s.Dot(s.Danger, "daemon parado"))
	case st != nil:
		if st.Override != nil {
			ov := st.Override.Issue
			chips = append(chips, s.Faint.Render("fijada ")+s.Bold.Render(fmt.Sprintf("%s#%d", shortRepo(ov.Repo.String(), m.sh.org), ov.Number)))
		}
		switch {
		case st.LastSyncError != "":
			chips = append(chips, s.Dot(s.Warning, "error al sincronizar"))
		case st.Pending > 0:
			chips = append(chips, s.Dot(s.Warning, fmt.Sprintf("%d sin sincronizar", st.Pending)))
		default:
			chips = append(chips, s.Dot(s.Success, "al día"))
		}
		if st.User != nil {
			who := st.User.Name
			if st.User.GitHubLogin != "" {
				who = "@" + st.User.GitHubLogin
			}
			chips = append(chips, s.Faint.Render(who))
		}
	default:
		chips = append(chips, s.Dot(s.Subtle, "conectando…"))
	}
	return spread(left, strings.Join(chips, "   ")+" ", w)
}

func (m *model) bannerView(w int) string {
	if w == 0 {
		return ""
	}
	s := m.sh.st
	var text string
	icon := s.Warn.Render("▲")
	switch {
	case m.sh.statusErr != nil && classify(m.sh.statusErr) == errDaemonDown:
		icon = s.Err.Render("▲")
		text = s.Base.Render("El daemon no está en marcha, no se está registrando el gasto. ") + s.Faint.Render("Arráncalo con ") + s.Key.Render("forge daemon install")
	case m.sh.status != nil && !m.sh.status.LoggedIn:
		text = s.Base.Render("No has iniciado sesión: el gasto se guarda pero no se sincroniza. ") + s.Faint.Render("Ejecuta ") + s.Key.Render("forge login")
	default:
		return ""
	}
	return " " + icon + " " + trunc(text, w-4)
}

// statusLine shows the latest notification, on one line above the hints.
func (m *model) statusLine(w int) string {
	if len(m.toasts) == 0 {
		return ""
	}
	s := m.sh.st
	t := m.toasts[len(m.toasts)-1]
	icon, text := s.Faint.Render("•"), s.Base.Render(t.text)
	switch t.kind {
	case toastOK:
		icon = s.OK.Render("✓")
	case toastWarn:
		icon = s.Warn.Render("▲")
	case toastErr:
		icon, text = s.Err.Render("✗"), s.Err.Render(t.text)
	}
	return " " + icon + " " + trunc(text, w-4)
}

// footerView shows the screen's main bindings on the left and the global
// ones on the right.
func (m *model) footerView(w int) string {
	s := m.sh.st
	sc := m.screens[m.active]
	right := hint(s, "?", "ayuda") + "   " + hint(s, "q", "salir")
	if sc.Capturing() {
		right = ""
	}
	left := hints(s, w-lipgloss.Width(right)-6, sc.Hints()...)
	return spread(" "+left, right+" ", w)
}

func (m *model) splashView(w, h int) string {
	s := m.sh.st
	lines := []string{
		s.Hi.Render(logo), "",
		s.Faint.Render("Cuánto cuesta cada issue en IA"), "",
		m.splashSpin.View() + " " + s.Faint.Render("Conectando con el daemon"),
	}
	if m.version != "" {
		lines = append(lines, "", s.Dim.Render(m.version))
	}
	return center(lipgloss.JoinVertical(lipgloss.Center, lines...), w, h)
}

// newShared builds the shared state.
func newShared(ctx context.Context, be Backend, th theme.Theme, org string, cur domain.Currency, exportDir string) *shared {
	return &shared{
		ctx: ctx, be: be, theme: th, st: th.Styles(true), org: org, currency: cur,
		now: time.Now, exportDir: exportDir, titles: map[string]string{},
	}
}
