package tui

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/harmonica"

	"github.com/hiberus-industria/forge/internal/daemon/localapi"
	"github.com/hiberus-industria/forge/internal/domain"
)

var liveKeys = struct{ Reconnect key.Binding }{
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

	feed          rowList[localapi.LiveEvent]
	width, height int

	// Animated session cost (micros).
	spring    harmonica.Spring
	shown     float64
	vel       float64
	animating bool
}

func newLiveScreen(sh *shared) *liveScreen {
	return &liveScreen{
		sh: sh, spring: harmonica.NewSpring(harmonica.FPS(60), 7.0, 0.9), backoff: time.Second,
		feed: newRowList(sh.st, func(e localapi.LiveEvent) string { return e.Model }),
	}
}

// liveChrome is the height above the feed: toolbar, gap, attribution (2),
// session, gap, chart (3), gap, column titles.
const liveChrome = 11

func (m *liveScreen) SetSize(w, h int) {
	m.width, m.height = w, h
	m.feed.SetHeight(h - liveChrome)
}

// syncFeed refreshes the feed items from the events.
func (m *liveScreen) syncFeed() { m.feed.SetItems(m.events, nil) }

func (m *liveScreen) Hints() []key.Binding {
	if !m.connected {
		return []key.Binding{liveKeys.Reconnect, navKeys.UpDown}
	}
	return []key.Binding{navKeys.UpDown}
}

func (m *liveScreen) Help() []helpGroup {
	return []helpGroup{
		{"Moverse", []key.Binding{navKeys.Up, navKeys.Down, navKeys.PgUp, navKeys.PgDown, navKeys.Home}},
		{"Conexión", []key.Binding{liveKeys.Reconnect}},
	}
}

func (m *liveScreen) Capturing() bool { return false }

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
			m.syncFeed()
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
		// Keep the selected row while reading older entries.
		cur := m.feed.Cursor()
		m.syncFeed()
		if cur > 0 {
			m.feed.Move(1)
		}
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
		m.feed.Wheel(msg)
	case tea.MouseClickMsg:
		m.feed.Click(msg.Y - liveChrome)
	case tea.KeyPressMsg:
		if key.Matches(msg, liveKeys.Reconnect) {
			m.connected = false
			return m, tea.Batch(m.connect(), toast(toastInfo, "Reconectando con el daemon…"))
		}
		m.feed.Nav(msg)
	}
	return m, nil
}

// ---------------------------------------------------------------- view

var sourceLabels = map[domain.AttributionSource]string{
	domain.SourceBranch:     "según la rama",
	domain.SourceManual:     "fijada a mano",
	domain.SourceReassigned: "reasignada",
	domain.SourceNone:       "sin tarea: irá a la bandeja",
}

func (m *liveScreen) View() string {
	s := m.sh.st
	width, height := m.width, m.height
	if len(m.events) == 0 {
		return m.emptyView(width, height)
	}

	// Toolbar: connection on the left, today's total on the right.
	conn := s.Dot(s.Success, "En directo")
	if !m.connected {
		conn = s.Dot(s.Warning, "Sin conexión, reintentando…")
	}
	last := m.events[0]
	conn += s.Faint.Render("  ·  " + harnessName(string(last.Harness)))
	toolbar := spread(conn, s.Faint.Render("Hoy  ")+s.Bold.Render(m.sh.money(int64(m.todayTotal()))), width)

	// Attribution: the pinned task wins, else the latest event.
	attr := last.Attribution
	src := attr.Source
	if st := m.sh.status; st != nil && st.Override != nil {
		ov := st.Override.Issue
		attr = domain.Attribution{Bucket: domain.BucketIssue, Source: domain.SourceManual, Repo: &ov.Repo, Issue: &ov, Branch: attr.Branch}
		src = domain.SourceManual
	}
	const labelW = 13
	label := func(t string) string { return s.Faint.Render(pad(t, labelW)) }
	var target string
	switch attr.Bucket {
	case domain.BucketIssue:
		ref := fmt.Sprintf("%s#%d", shortRepo(attr.Issue.Repo.String(), m.sh.org), attr.Issue.Number)
		target = s.Hi.Render(ref)
		if t := m.sh.title(attr.Issue.String()); t != "" {
			target += "  " + s.Base.Render(t)
		}
	case domain.BucketRepo:
		target = s.Warn.Render(shortRepo(attr.Repo.String(), m.sh.org) + " · sin tarea")
	default:
		target = s.Warn.Render("Fuera de un repositorio")
	}
	why := sourceLabels[src]
	if attr.Branch != "" {
		why = "rama " + attr.Branch + " · " + why
	}
	who := label("Imputando a") + trunc(target, width-labelW) + "\n" +
		pad("", labelW) + s.Faint.Render(trunc(why, width-labelW))

	cost, toks, n := m.sessionTotals()
	counter := s.Bold.Render(m.sh.money(int64(m.shown)))
	if alt := m.sh.moneyAlt(int64(cost)); alt != "" {
		counter += s.Faint.Render("  ≈ " + alt)
	}
	session := label("Esta sesión") + counter + s.Faint.Render("   "+plural(n, "petición", "peticiones")+" · "+humanTokens(toks.Total())+" tokens")

	// Cost per minute with its time axis.
	vals := m.perMinute()
	peak := 0.0
	for _, v := range vals {
		peak = max(peak, v)
	}
	chartTitle := spread(s.Section.Render("Coste por minuto"), s.Dim.Render("máx "+m.sh.money(int64(peak))), width)
	chart := sparkline(stretch(vals, width), s.Primary, s.Border)
	axis := spread(s.Dim.Render(fmt.Sprintf("hace %d min", sparkMinutes)), s.Dim.Render("ahora"), width)

	return strings.Join([]string{
		toolbar, "", who, session, "", chartTitle, chart, axis, "",
		headerRow(s, width, m.feedCols(nil, false, width)...), m.feedView(width),
	}, "\n")
}

func (m *liveScreen) feedCols(e *localapi.LiveEvent, sel bool, width int) []col {
	modelW := 14
	if width < 70 {
		modelW = 10
	}
	if e == nil {
		return []col{
			{text: "hora", w: 8},
			{text: "modelo", w: modelW},
			{text: "entrada", w: 8, right: true},
			{text: "salida", w: 7, right: true},
			{text: "coste", w: 9, right: true},
			{text: "", w: 1},
			{text: "tarea"},
		}
	}
	s := m.sh.st
	var where string
	whereSt := s.Base
	switch e.Attribution.Bucket {
	case domain.BucketIssue:
		where = fmt.Sprintf("%s#%d", shortRepo(e.Attribution.Issue.Repo.String(), m.sh.org), e.Attribution.Issue.Number)
	case domain.BucketRepo:
		where, whereSt = shortRepo(e.Attribution.Repo.String(), m.sh.org)+" · bandeja", s.Warn
	default:
		where, whereSt = "bandeja", s.Warn
	}
	sync, syncSt := " ", s.Dim
	if !e.Synced {
		sync, syncSt = "⧗", s.Warn
	}
	return []col{
		{text: e.OccurredAt.Local().Format("15:04:05"), w: 8, st: s.Faint},
		{text: shortModel(e.Model), w: modelW, st: s.Base},
		{text: humanTokens(e.Tokens.Input + e.Tokens.CacheRead + e.Tokens.CacheWrite), w: 8, right: true, st: s.Faint},
		{text: humanTokens(e.Tokens.Output + e.Tokens.Reasoning), w: 7, right: true, st: s.Faint},
		{text: m.sh.money(int64(e.CostMicros)), w: 9, right: true, st: s.Base},
		{text: sync, w: 1, st: syncSt},
		{text: where, st: whereSt},
	}
}

func (m *liveScreen) feedView(width int) string {
	return m.feed.View(width, func(e localapi.LiveEvent, sel bool, w int) string {
		return row(m.sh.st, w, sel, m.feedCols(&e, sel, w)...)
	})
}

func (m *liveScreen) emptyView(width, height int) string {
	s := m.sh.st
	status := s.Dot(s.Success, "Escuchando a Claude Code y OpenCode")
	if !m.connected && m.connErr != nil {
		status = s.Dot(s.Warning, "Sin conexión con el daemon")
	}
	steps := lipgloss.JoinVertical(lipgloss.Left,
		s.Key.Render(pad("forge setup", 18))+s.Faint.Render("conecta Claude Code y OpenCode"),
		s.Key.Render(pad("forge doctor", 18))+s.Faint.Render("comprueba que todo funciona"),
		s.Key.Render(pad("forge start 123", 18))+s.Faint.Render("empieza el issue 123 en su rama"),
	)
	content := lipgloss.JoinVertical(lipgloss.Left,
		s.Bold.Render("Todavía no hay actividad"), status, "",
		s.Faint.Render("Cada petición a la IA aparecerá aquí al momento, con su coste y su tarea."), "",
		s.Section.Render("¿Primera vez?"), steps)
	return center(content, width, height)
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
