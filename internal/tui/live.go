package tui

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/harmonica"

	"github.com/hiberus-industria/forge/internal/daemon/localapi"
	"github.com/hiberus-industria/forge/internal/domain"
)

type liveKeys struct{ Up, Down, Reconnect key.Binding }

func (k liveKeys) ShortHelp() []key.Binding  { return []key.Binding{k.Up, k.Down, k.Reconnect} }
func (k liveKeys) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }

var liveKeyMap = liveKeys{
	Up:        key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "subir")),
	Down:      key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "bajar")),
	Reconnect: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reconectar")),
}

const (
	liveCap      = 500
	sparkMinutes = 30
)

type liveRecentMsg struct {
	events []localapi.LiveEvent
	err    error
}

type liveConnectedMsg struct {
	gen int
	ch  <-chan localapi.LiveEvent
}

type liveEventMsg struct {
	gen int
	ev  localapi.LiveEvent
}

type liveClosedMsg struct {
	gen int
	err error
}

type liveRetryMsg struct{ gen int }

type animTickMsg struct{}

type liveScreen struct {
	sh *shared

	events    []localapi.LiveEvent // newest first
	gen       int                  // subscription generation
	ch        <-chan localapi.LiveEvent
	connected bool
	backoff   time.Duration
	connErr   error
	scroll    int

	// Animated session cost (micros).
	spring    harmonica.Spring
	shown     float64
	vel       float64
	animating bool
	flash     time.Time // last event arrival, for the pulse
}

func newLiveScreen(sh *shared) *liveScreen {
	return &liveScreen{sh: sh, spring: harmonica.NewSpring(harmonica.FPS(60), 7.0, 0.9), backoff: time.Second}
}

func (m *liveScreen) Keys() help.KeyMap { return liveKeyMap }
func (m *liveScreen) Capturing() bool   { return false }

func (m *liveScreen) Init() tea.Cmd { return tea.Batch(m.loadRecent(), m.connect()) }

func (m *liveScreen) loadRecent() tea.Cmd {
	be, ctx := m.sh.be, m.sh.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		evs, err := be.Recent(ctx, 200)
		return liveRecentMsg{events: evs, err: err}
	}
}

func (m *liveScreen) connect() tea.Cmd {
	m.gen++
	gen, be, ctx := m.gen, m.sh.be, m.sh.ctx
	return func() tea.Msg {
		ch, err := be.Live(ctx)
		if err != nil {
			return liveClosedMsg{gen: gen, err: err}
		}
		return liveConnectedMsg{gen: gen, ch: ch}
	}
}

func waitLive(gen int, ch <-chan localapi.LiveEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return liveClosedMsg{gen: gen}
		}
		return liveEventMsg{gen: gen, ev: ev}
	}
}

func animTick() tea.Cmd {
	return tea.Tick(time.Second/60, func(time.Time) tea.Msg { return animTickMsg{} })
}

// sessionID is the session of the most recent event.
func (m *liveScreen) sessionID() string {
	if len(m.events) == 0 {
		return ""
	}
	return m.events[0].SessionID
}

// sessionTotals sums cost and tokens of the current session.
func (m *liveScreen) sessionTotals() (domain.Micros, domain.Tokens, int) {
	sid := m.sessionID()
	var c domain.Micros
	var t domain.Tokens
	n := 0
	for _, e := range m.events {
		if e.SessionID == sid {
			c += e.CostMicros
			t = t.Add(e.Tokens)
			n++
		}
	}
	return c, t, n
}

func (m *liveScreen) todayTotal() domain.Micros {
	now := m.sh.now()
	y, mo, d := now.Date()
	var c domain.Micros
	for _, e := range m.events {
		ey, em, ed := e.OccurredAt.In(now.Location()).Date()
		if ey == y && em == mo && ed == d {
			c += e.CostMicros
		}
	}
	return c
}

// perMinute buckets cost into the last sparkMinutes minutes.
func (m *liveScreen) perMinute() []float64 {
	now := m.sh.now()
	vals := make([]float64, sparkMinutes)
	for _, e := range m.events {
		ago := int(now.Sub(e.OccurredAt).Minutes())
		if ago >= 0 && ago < sparkMinutes {
			vals[sparkMinutes-1-ago] += float64(e.CostMicros)
		}
	}
	return vals
}

func (m *liveScreen) retarget() tea.Cmd {
	c, _, _ := m.sessionTotals()
	if math.Abs(float64(c)-m.shown) > 0.5 && !m.animating {
		m.animating = true
		return animTick()
	}
	return nil
}

func (m *liveScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case liveRecentMsg:
		if msg.err == nil && len(msg.events) > 0 && len(m.events) == 0 {
			m.events = msg.events
			if len(m.events) > liveCap {
				m.events = m.events[:liveCap]
			}
			c, _, _ := m.sessionTotals()
			m.shown = float64(c)
		}
	case liveConnectedMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.connected, m.connErr, m.backoff, m.ch = true, nil, time.Second, msg.ch
		return m, waitLive(msg.gen, msg.ch)
	case liveEventMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.events = append([]localapi.LiveEvent{msg.ev}, m.events...)
		if len(m.events) > liveCap {
			m.events = m.events[:liveCap]
		}
		if m.scroll > 0 {
			m.scroll++ // keep the viewport stable while reading older entries
		}
		m.flash = m.sh.now()
		// A new session resets the counter animation from zero.
		if len(m.events) > 1 && m.events[1].SessionID != msg.ev.SessionID {
			m.shown, m.vel = 0, 0
		}
		return m, tea.Batch(m.retarget(), waitLive(msg.gen, m.ch))
	case liveClosedMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.connected = false
		m.connErr = msg.err
		d := m.backoff
		m.backoff = min(m.backoff*2, 30*time.Second)
		gen := m.gen
		return m, tea.Tick(d, func(time.Time) tea.Msg { return liveRetryMsg{gen} })
	case liveRetryMsg:
		if msg.gen == m.gen && !m.connected {
			return m, m.connect()
		}
	case animTickMsg:
		c, _, _ := m.sessionTotals()
		target := float64(c)
		m.shown, m.vel = m.spring.Update(m.shown, m.vel, target)
		if math.Abs(target-m.shown) < 0.5 && math.Abs(m.vel) < 1 {
			m.shown, m.vel, m.animating = target, 0, false
			return m, nil
		}
		return m, animTick()
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.scroll = max(0, m.scroll-1)
		case tea.MouseWheelDown:
			m.scroll = min(max(0, len(m.events)-1), m.scroll+1)
		}
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, liveKeyMap.Up):
			m.scroll = max(0, m.scroll-1)
		case key.Matches(msg, liveKeyMap.Down):
			m.scroll = min(max(0, len(m.events)-1), m.scroll+1)
		case key.Matches(msg, liveKeyMap.Reconnect):
			m.connected = false
			return m, tea.Batch(m.connect(), toast(toastInfo, "Reconectando con el daemon…"))
		}
	}
	return m, nil
}

// ---------------------------------------------------------------- view

func (m *liveScreen) View(width, height int) string {
	s := m.sh.st
	if len(m.events) == 0 {
		return m.emptyView(width, height)
	}

	// Attribution card: the override wins, else the latest event.
	last := m.events[0]
	attr := last.Attribution
	src := attr.Source
	if st := m.sh.status; st != nil && st.Override != nil {
		ov := st.Override.Issue
		attr = domain.Attribution{Bucket: domain.BucketIssue, Source: domain.SourceManual, Repo: &ov.Repo, Issue: &ov}
		src = domain.SourceManual
	}
	gap := 1
	cardW := (width - 2*gap) / 3
	var target string
	switch attr.Bucket {
	case domain.BucketIssue:
		target = s.GradientText(fmt.Sprintf("%s#%d", shortRepo(attr.Issue.Repo.String(), m.sh.org), attr.Issue.Number), true)
	case domain.BucketRepo:
		target = lipgloss.NewStyle().Foreground(s.Warning).Bold(true).Render(shortRepo(attr.Repo.String(), m.sh.org)) + s.Faint.Render(" · sin tarea")
	default:
		target = lipgloss.NewStyle().Foreground(s.Warning).Bold(true).Render("personal / otros")
	}
	srcLabel := map[domain.AttributionSource]string{
		domain.SourceBranch: "por rama", domain.SourceManual: "manual", domain.SourceReassigned: "reasignado", domain.SourceNone: "sin asignar",
	}[src]
	attrBody := s.Faint.Render("imputando a") + "\n" + target + "\n"
	if attr.Branch != "" {
		attrBody += s.Faint.Render("⎇ ") + s.Base.Render(trunc(attr.Branch, cardW-6)) + "\n"
	} else {
		attrBody += "\n"
	}
	dot := s.Warning
	if attr.Bucket == domain.BucketIssue {
		dot = s.Success
	}
	attrBody += s.Chip(dot, srcLabel)

	cost, toks, n := m.sessionTotals()
	pulse := s.Primary
	if m.sh.now().Sub(m.flash) < 700*time.Millisecond {
		pulse = s.Accent
	}
	counter := lipgloss.NewStyle().Foreground(pulse).Bold(true).Render(m.sh.money(int64(m.shown)))
	costBody := s.Faint.Render("sesión actual") + "\n" + counter
	if alt := m.sh.moneyAlt(int64(cost)); alt != "" {
		costBody += s.Faint.Render("  ≈ " + alt)
	}
	costBody += "\n" + s.Faint.Render(fmt.Sprintf("%d peticiones · %s tokens", n, humanTokens(toks.Total()))) + "\n" +
		s.Faint.Render("hoy ") + s.Bold.Render(m.sh.money(int64(m.todayTotal())))

	attrCard := s.Card.Width(cardW).Height(6).Render(attrBody)
	costCard := s.Card.Width(cardW).Height(6).Render(costBody)
	tokCard := s.Card.Width(width - 2*cardW - 2*gap).Height(6).Render(
		strings.TrimRight(tokenBarsDomain(m.sh, toks, width-2*cardW-2*gap-4), "\n"))
	top := lipgloss.JoinHorizontal(lipgloss.Top, attrCard, strings.Repeat(" ", gap), costCard, strings.Repeat(" ", gap), tokCard)

	// Sparkline of cost per minute.
	vals := m.perMinute()
	sparkW := width - 26
	stretched := stretch(vals, sparkW)
	spark := s.Faint.Render(pad("últimos 30 min", 16)) + sparkline(stretched, s.Primary) + " " + s.Faint.Render("coste/min")

	// Feed.
	feedH := height - lipgloss.Height(top) - 4
	header := s.Faint.Render(pad("hora", 9) + "  " + pad("modelo", 20) + pad("tokens", 16) + padLeft("coste", 10) + "  " + "tarea")
	var feed []string
	start := min(m.scroll, max(0, len(m.events)-1))
	for i := start; i < len(m.events) && len(feed) < feedH; i++ {
		feed = append(feed, m.feedLine(m.events[i], width))
	}
	conn := s.Chip(s.Success, "en directo")
	if !m.connected {
		conn = s.Chip(s.Warning, "reconectando…")
	}
	title := spread(s.Title.Render("Actividad"), conn, width)
	return top + "\n" + spark + "\n" + title + "\n" + header + "\n" + strings.Join(feed, "\n")
}

func (m *liveScreen) feedLine(e localapi.LiveEvent, width int) string {
	s := m.sh.st
	t := s.Faint.Render(e.OccurredAt.Local().Format("15:04:05"))
	model := harnessIcon(s, string(e.Harness)) + " " + pad(shortModel(e.Model), 18)
	tok := pad(fmt.Sprintf("%s→%s", humanTokens(e.Tokens.Input+e.Tokens.CacheRead+e.Tokens.CacheWrite), humanTokens(e.Tokens.Output+e.Tokens.Reasoning)), 16)
	cost := padLeft(s.Bold.Render(m.sh.money(int64(e.CostMicros))), 10)
	var where string
	switch e.Attribution.Bucket {
	case domain.BucketIssue:
		where = lipgloss.NewStyle().Foreground(s.Secondary).Render(fmt.Sprintf("%s#%d", shortRepo(e.Attribution.Issue.Repo.String(), m.sh.org), e.Attribution.Issue.Number))
	case domain.BucketRepo:
		where = lipgloss.NewStyle().Foreground(s.Warning).Render(shortRepo(e.Attribution.Repo.String(), m.sh.org) + " · bandeja")
	default:
		where = lipgloss.NewStyle().Foreground(s.Warning).Render("personal · bandeja")
	}
	sync := lipgloss.NewStyle().Foreground(s.Success).Render("✓")
	if !e.Synced {
		sync = lipgloss.NewStyle().Foreground(s.Warning).Render("⧗")
	}
	line := t + "  " + model + tok + cost + "  " + sync + " " + where
	return trunc(line, width)
}

func (m *liveScreen) emptyView(width, height int) string {
	s := m.sh.st
	pulse := []string{"◜", "◝", "◞", "◟"}[int(m.sh.now().UnixMilli()/250)%4]
	var b strings.Builder
	b.WriteString(s.GradientText("Aún no hay actividad", true) + "\n\n")
	if !m.connected && m.connErr != nil {
		b.WriteString(lipgloss.NewStyle().Foreground(s.Warning).Render(pulse+" sin conexión con el daemon") + "\n\n")
	} else {
		b.WriteString(s.Faint.Render(pulse+" escuchando a Claude Code y OpenCode…") + "\n\n")
	}
	steps := []string{
		s.Key.Render("forge setup") + s.Faint.Render("   conecta tus harnesses (muestra el diff antes)"),
		s.Key.Render("forge doctor") + s.Faint.Render("  comprueba que la captura funciona"),
		s.Key.Render("forge start 123") + s.Faint.Render(" crea la rama feat/123-… y empieza a imputar"),
	}
	b.WriteString(strings.Join(steps, "\n"))
	b.WriteString("\n\n" + s.Faint.Render("Cada petición aparecerá aquí al instante, con su coste y su tarea."))
	return center(b.String(), width, height)
}

// tokenBarsDomain adapts domain tokens to the shared token bars.
func tokenBarsDomain(sh *shared, t domain.Tokens, width int) string {
	return tokenBars(sh, apiTokens(t), width)
}

// stretch resamples values to n buckets (nearest neighbour).
func stretch(vals []float64, n int) []float64 {
	if n <= 0 || len(vals) == 0 {
		return nil
	}
	out := make([]float64, n)
	for i := range out {
		out[i] = vals[i*len(vals)/n]
	}
	return out
}
