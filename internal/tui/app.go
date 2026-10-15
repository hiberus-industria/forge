package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
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
	Tabs: key.NewBinding(key.WithKeys("1", "2", "3", "4"), key.WithHelp("1-4", "pestañas")),
	Next: key.NewBinding(key.WithKeys("tab", "shift+tab"), key.WithHelp("tab", "siguiente")),
	Help: key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "ayuda")),
	Quit: key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "salir")),
}

// footerKeys merges the screen bindings with the global ones.
type footerKeys struct {
	screen help.KeyMap
}

func (f footerKeys) ShortHelp() []key.Binding {
	return append(f.screen.ShortHelp(), globalKeyMap.Help, globalKeyMap.Quit)
}

func (f footerKeys) FullHelp() [][]key.Binding {
	return append(f.screen.FullHelp(), []key.Binding{globalKeyMap.Tabs, globalKeyMap.Next, globalKeyMap.Help, globalKeyMap.Quit})
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
	help     help.Model
	toasts   []toastItem
	toastSeq int

	splash     bool
	splashSpin spinner.Model
}

func newModel(sh *shared, version string) *model {
	m := &model{
		sh:      sh,
		version: version,
		titles:  []string{"Tareas", "En vivo", "Bandeja", "Informes"},
		help:    help.New(),
		splash:  true,
	}
	m.screens = []screen{newTasksScreen(sh), newLiveScreen(sh), newInboxScreen(sh), newReportsScreen(sh)}
	m.splashSpin = spinner.New(spinner.WithSpinner(spinner.Points))
	m.applyStyles()
	return m
}

func (m *model) applyStyles() {
	s := m.sh.st
	hs := help.DefaultStyles(s.IsDark)
	hs.ShortKey = s.Key
	hs.FullKey = s.Key
	hs.ShortDesc = s.Faint
	hs.FullDesc = s.Faint
	hs.ShortSeparator = s.Dim
	hs.FullSeparator = s.Dim
	m.help.Styles = hs
	m.splashSpin.Style = lipgloss.NewStyle().Foreground(s.Primary)
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
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.sh.width, m.sh.height = msg.Width, msg.Height
		m.help.SetWidth(msg.Width - 2)
	case tea.BackgroundColorMsg:
		m.sh.st = m.sh.theme.Styles(msg.IsDark())
		m.applyStyles()
		return m, nil
	case statusMsg:
		m.splash = false
		if msg.err == nil {
			st := msg.st
			m.sh.status, m.sh.statusErr = &st, nil
		} else {
			m.sh.statusErr = msg.err
		}
		return m, nil
	case statusTickMsg:
		return m, tea.Batch(statusNow(m.sh), m.statusTick())
	case fxMsg:
		m.sh.usdPerEUR = msg.usdPerEUR
		return m, nil
	case splashDoneMsg:
		m.splash = false
		return m, nil
	case spinner.TickMsg:
		if m.splash && msg.ID == m.splashSpin.ID() {
			var cmd tea.Cmd
			m.splashSpin, cmd = m.splashSpin.Update(msg)
			return m, cmd
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
			ttl = 7 * time.Second
		}
		return m, tea.Tick(ttl, func(time.Time) tea.Msg { return toastExpireMsg{id} })
	case toastExpireMsg:
		for i, t := range m.toasts {
			if t.id == msg.id {
				m.toasts = append(m.toasts[:i], m.toasts[i+1:]...)
				break
			}
		}
		return m, nil
	case errMsg:
		switch classify(msg.err) {
		case errDaemonDown:
			m.sh.statusErr = msg.err
		}
		return m, toast(toastErr, msg.err.Error())
	case openModalMsg:
		m.modal = msg.m
		return m, nil
	case closeModalMsg:
		m.modal = nil
		return m, nil
	case tea.KeyPressMsg:
		return m, m.handleKey(msg)
	case tea.MouseClickMsg:
		if msg.Y == 0 && m.modal == nil {
			if i := m.tabAt(msg.X); i >= 0 {
				m.active = i
			}
		}
		return m, nil
	case tea.MouseMsg:
		if m.modal == nil {
			var cmd tea.Cmd
			m.screens[m.active], cmd = m.screens[m.active].Update(msg)
			return m, cmd
		}
		return m, nil
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
	return m, tea.Batch(cmds...)
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
	if m.splash {
		m.splash = false
	}
	switch k.String() {
	case "q":
		return tea.Quit
	case "?":
		m.help.ShowAll = !m.help.ShowAll
		return nil
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

	header := m.headerView(w)
	banner := m.bannerView(w)
	footKeys := footerKeys{m.screens[m.active].Keys()}
	footer := " " + m.help.View(footKeys)
	used := lipgloss.Height(header) + lipgloss.Height(footer) + 1
	if banner != "" {
		used += lipgloss.Height(banner)
	}
	bodyH := max(3, h-used)
	body := m.screens[m.active].View(w-2, bodyH)
	body = lipgloss.NewStyle().Padding(0, 1).Render(fitHeight(body, bodyH))

	parts := []string{header}
	if banner != "" {
		parts = append(parts, banner)
	}
	parts = append(parts, body, s.Dim.Render(strings.Repeat("─", w)), footer)
	base := fitHeight(strings.Join(parts, "\n"), h)

	var layers []*lipgloss.Layer
	if m.modal != nil {
		mv := m.modal.View(s, w)
		mw, mh := lipgloss.Width(mv), lipgloss.Height(mv)
		layers = append(layers, lipgloss.NewLayer(mv).X(max(0, (w-mw)/2)).Y(max(0, (h-mh)/3)).Z(1))
	}
	if tv := m.toastsView(); tv != "" {
		tw, th := lipgloss.Width(tv), lipgloss.Height(tv)
		layers = append(layers, lipgloss.NewLayer(tv).X(max(0, w-tw-1)).Y(max(0, h-th-2)).Z(2))
	}
	if len(layers) == 0 {
		return base
	}
	all := append([]*lipgloss.Layer{lipgloss.NewLayer(base)}, layers...)
	return fitHeight(lipgloss.NewCompositor(all...).Render(), h)
}

const brand = "◆ forge"

// tabLabels returns the rendered tabs and their x ranges (for mouse clicks).
func (m *model) tabLabels() ([]string, [][2]int) {
	s := m.sh.st
	x := lipgloss.Width(brand) + 3
	var out []string
	var ranges [][2]int
	for i, t := range m.titles {
		label := fmt.Sprintf("%d %s", i+1, t)
		var r string
		if i == m.active {
			r = s.TabOn.Render(label)
		} else {
			r = s.TabOff.Render(label)
		}
		out = append(out, r)
		w := lipgloss.Width(r)
		ranges = append(ranges, [2]int{x, x + w})
		x += w + 1
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
	left := " " + s.GradientText(brand, true) + "  " + strings.Join(tabs, " ")

	var chips []string
	st := m.sh.status
	switch {
	case m.sh.statusErr != nil && classify(m.sh.statusErr) == errDaemonDown:
		chips = append(chips, s.Chip(s.Danger, "daemon parado"))
	case st != nil:
		if st.Override != nil {
			ov := st.Override.Issue
			chips = append(chips, lipgloss.NewStyle().Foreground(s.Accent).Bold(true).Render(
				fmt.Sprintf("⚑ %s#%d", shortRepo(ov.Repo.String(), m.sh.org), ov.Number)))
		}
		if st.Pending > 0 {
			chips = append(chips, lipgloss.NewStyle().Foreground(s.Warning).Render(fmt.Sprintf("⇡%d", st.Pending)))
		}
		if st.LastSyncError != "" {
			chips = append(chips, s.Chip(s.Warning, "sync con errores"))
		} else {
			chips = append(chips, s.Chip(s.Success, "daemon"))
		}
		if st.User != nil {
			who := st.User.Name
			if st.User.GitHubLogin != "" {
				who = "@" + st.User.GitHubLogin
			}
			chips = append(chips, s.Faint.Render(who))
		}
	default:
		chips = append(chips, s.Chip(s.Muted, "conectando…"))
	}
	right := strings.Join(chips, s.Dim.Render("  ·  ")) + " "
	return spread(left, right, w)
}

func (m *model) bannerView(w int) string {
	s := m.sh.st
	var text string
	c := s.Warning
	switch {
	case m.sh.statusErr != nil && classify(m.sh.statusErr) == errDaemonDown:
		c = s.Danger
		text = "El daemon de forge no está en marcha. Arráncalo con " + s.Bold.Render("forge daemon install") + " (o " + s.Bold.Render("forge daemon run") + ")."
	case m.sh.status != nil && !m.sh.status.LoggedIn:
		text = "No has iniciado sesión. Ejecuta " + s.Bold.Render("forge login") + " para sincronizar tu gasto."
	default:
		return ""
	}
	return lipgloss.NewStyle().Foreground(c).Bold(true).Render(" ▲ ") + trunc(text, w-4)
}

func (m *model) toastsView() string {
	if len(m.toasts) == 0 {
		return ""
	}
	s := m.sh.st
	var out []string
	for _, t := range m.toasts {
		icon, c := "ℹ", s.Info
		switch t.kind {
		case toastOK:
			icon, c = "✔", s.Success
		case toastWarn:
			icon, c = "▲", s.Warning
		case toastErr:
			icon, c = "✗", s.Danger
		}
		maxW := min(60, max(24, m.sh.width/2))
		body := lipgloss.NewStyle().Foreground(c).Bold(true).Render(icon) + " " + s.Base.Width(maxW-6).Render(t.text)
		out = append(out, lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c).Padding(0, 1).Render(body))
	}
	return lipgloss.JoinVertical(lipgloss.Right, out...)
}

func (m *model) splashView(w, h int) string {
	s := m.sh.st
	tag := s.Faint.Render("trazabilidad del gasto IA · repo → issue → PR")
	ver := ""
	if m.version != "" {
		ver = s.Dim.Render(m.version)
	}
	content := lipgloss.JoinVertical(lipgloss.Center,
		s.GradientBlock(logo), "", tag, "", m.splashSpin.View()+" "+s.Faint.Render("conectando con el daemon"), ver)
	return center(content, w, h)
}

// newShared builds the shared state.
func newShared(ctx context.Context, be Backend, th theme.Theme, org string, cur domain.Currency, exportDir string) *shared {
	return &shared{
		ctx: ctx, be: be, theme: th, st: th.Styles(true), org: org, currency: cur,
		now: time.Now, exportDir: exportDir,
	}
}
