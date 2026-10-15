package tui

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/domain"
)

type reportKeys struct{ Group, Period, Custom, Currency, Export, Up, Down, Refresh key.Binding }

func (k reportKeys) ShortHelp() []key.Binding {
	return []key.Binding{k.Group, k.Period, k.Currency, k.Export, k.Custom}
}

func (k reportKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Group, k.Period, k.Custom}, {k.Currency, k.Export, k.Refresh}, {k.Up, k.Down}}
}

var reportKeyMap = reportKeys{
	Group:    key.NewBinding(key.WithKeys("g"), key.WithHelp("g/G", "agrupar")),
	Period:   key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "periodo")),
	Custom:   key.NewBinding(key.WithKeys("P"), key.WithHelp("P", "periodo a medida")),
	Currency: key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "USD/EUR")),
	Export:   key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "exportar CSV")),
	Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "subir")),
	Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "bajar")),
	Refresh:  key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "recargar")),
}

var groupBys = []struct {
	g     api.GroupBy
	label string
}{
	{api.GroupByRepo, "repositorio"},
	{api.GroupByIssue, "tarea"},
	{api.GroupByUser, "persona"},
	{api.GroupByModel, "modelo"},
	{api.GroupByHarness, "harness"},
	{api.GroupByDay, "día"},
}

// period is a [from,to) range.
type period struct {
	label    string
	from, to time.Time
}

// presetPeriods computes the presets relative to now.
func presetPeriods(now time.Time) []period {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	tomorrow := day.AddDate(0, 0, 1)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	return []period{
		{"últimos 7 días", day.AddDate(0, 0, -6), tomorrow},
		{"últimos 30 días", day.AddDate(0, 0, -29), tomorrow},
		{"este mes", month, month.AddDate(0, 1, 0)},
		{"mes anterior", month.AddDate(0, -1, 0), month},
	}
}

// parseCustomPeriod parses "2026-09-01..2026-09-30" (inclusive end day).
func parseCustomPeriod(s string, loc *time.Location) (period, error) {
	a, b, ok := strings.Cut(strings.ReplaceAll(s, " ", ""), "..")
	if !ok {
		return period{}, fmt.Errorf("formato: AAAA-MM-DD..AAAA-MM-DD")
	}
	from, err := time.ParseInLocation("2006-01-02", a, loc)
	if err != nil {
		return period{}, fmt.Errorf("fecha inicial no válida")
	}
	to, err := time.ParseInLocation("2006-01-02", b, loc)
	if err != nil {
		return period{}, fmt.Errorf("fecha final no válida")
	}
	if to.Before(from) {
		return period{}, fmt.Errorf("la fecha final es anterior a la inicial")
	}
	return period{label: a + " → " + b, from: from, to: to.AddDate(0, 0, 1)}, nil
}

type reportLoadedMsg struct {
	seq int
	r   api.CostReport
	err error
}

type customPeriodMsg struct{ p period }

type reportsScreen struct {
	sh       *shared
	group    int
	period   int // index in presets; -1 = custom
	custom   period
	currency domain.Currency
	report   *api.CostReport
	loading  bool
	err      error
	seq      int
	cursor   int
}

func newReportsScreen(sh *shared) *reportsScreen {
	return &reportsScreen{sh: sh, currency: sh.currency, loading: true}
}

func (m *reportsScreen) Keys() help.KeyMap { return reportKeyMap }
func (m *reportsScreen) Capturing() bool   { return false }
func (m *reportsScreen) Init() tea.Cmd     { return m.load() }

func (m *reportsScreen) currentPeriod() period {
	if m.period < 0 {
		return m.custom
	}
	return presetPeriods(m.sh.now())[m.period]
}

func (m *reportsScreen) params() api.GetCostReportParams {
	p := m.currentPeriod()
	cur := api.Currency(m.currency)
	from, to := p.from, p.to
	return api.GetCostReportParams{GroupBy: groupBys[m.group].g, Currency: &cur, From: &from, To: &to}
}

func (m *reportsScreen) load() tea.Cmd {
	m.seq++
	seq, p, be, ctx := m.seq, m.params(), m.sh.be, m.sh.ctx
	m.loading = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		r, err := be.Report(ctx, p)
		return reportLoadedMsg{seq: seq, r: r, err: err}
	}
}

// writeReportCSV exports the report to dir and returns the file path.
func writeReportCSV(dir string, r api.CostReport, p period) (string, error) {
	name := fmt.Sprintf("forge-%s-%s_%s.csv", r.GroupBy, p.from.Format("20060102"), p.to.AddDate(0, 0, -1).Format("20060102"))
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	_ = w.Write([]string{"key", "label", "cost", "currency", "cost_usd", "events", "input_tokens", "output_tokens", "reasoning_tokens", "cache_read_tokens", "cache_write_tokens"})
	for _, row := range r.Rows {
		_ = w.Write([]string{
			row.Key, row.Label,
			strconv.FormatFloat(row.Cost, 'f', 6, 64), string(r.Currency),
			strconv.FormatFloat(domain.Micros(row.CostUsdMicros).USD(), 'f', 6, 64),
			strconv.Itoa(row.Events),
			strconv.FormatInt(row.Tokens.Input, 10), strconv.FormatInt(row.Tokens.Output, 10),
			strconv.FormatInt(row.Tokens.Reasoning, 10), strconv.FormatInt(row.Tokens.CacheRead, 10),
			strconv.FormatInt(row.Tokens.CacheWrite, 10),
		})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return "", err
	}
	return path, f.Close()
}

func (m *reportsScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case reportLoadedMsg:
		if msg.seq != m.seq {
			return m, nil
		}
		m.loading, m.err = false, msg.err
		if msg.err == nil {
			r := msg.r
			m.report = &r
			m.cursor = min(m.cursor, max(0, len(r.Rows)-1))
		}
	case customPeriodMsg:
		m.period, m.custom = -1, msg.p
		return m, m.load()
	case refreshMsg:
		return m, m.load()
	case tea.MouseWheelMsg:
		if m.report != nil {
			switch msg.Button {
			case tea.MouseWheelUp:
				m.cursor = max(0, m.cursor-1)
			case tea.MouseWheelDown:
				m.cursor = min(max(0, len(m.report.Rows)-1), m.cursor+1)
			}
		}
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, reportKeyMap.Group):
			m.group = (m.group + 1) % len(groupBys)
			m.cursor = 0
			return m, m.load()
		case msg.String() == "G":
			m.group = (m.group - 1 + len(groupBys)) % len(groupBys)
			m.cursor = 0
			return m, m.load()
		case key.Matches(msg, reportKeyMap.Period):
			m.period = (m.period + 1) % len(presetPeriods(m.sh.now()))
			return m, m.load()
		case key.Matches(msg, reportKeyMap.Custom):
			loc := m.sh.now().Location()
			return m, openModal(newInputModal(m.sh.st, "Periodo a medida", "Fechas inclusivas, en tu zona horaria.", "2026-09-01..2026-09-30",
				func(v string) error { _, err := parseCustomPeriod(v, loc); return err },
				func(v string) tea.Cmd {
					p, _ := parseCustomPeriod(v, loc)
					return func() tea.Msg { return customPeriodMsg{p} }
				}))
		case key.Matches(msg, reportKeyMap.Currency):
			if m.currency == domain.CurrencyEUR {
				m.currency = domain.CurrencyUSD
			} else {
				m.currency = domain.CurrencyEUR
			}
			return m, m.load()
		case key.Matches(msg, reportKeyMap.Export):
			if m.report == nil {
				return m, nil
			}
			path, err := writeReportCSV(m.sh.exportDir, *m.report, m.currentPeriod())
			if err != nil {
				return m, toast(toastErr, "No se pudo exportar: "+err.Error())
			}
			return m, toast(toastOK, "CSV exportado → "+path)
		case key.Matches(msg, reportKeyMap.Refresh):
			return m, m.load()
		case key.Matches(msg, reportKeyMap.Up):
			m.cursor = max(0, m.cursor-1)
		case key.Matches(msg, reportKeyMap.Down):
			if m.report != nil {
				m.cursor = min(max(0, len(m.report.Rows)-1), m.cursor+1)
			}
		}
	}
	return m, nil
}

func (m *reportsScreen) fmtCost(v float64) string {
	return domain.FormatAmount(v, domain.Currency(m.currency))
}

func (m *reportsScreen) View(width, height int) string {
	s := m.sh.st
	// Selector line: segmented controls.
	seg := func(opts []string, active int) string {
		parts := make([]string, len(opts))
		for i, o := range opts {
			if i == active {
				parts[i] = s.TabOn.Render(o)
			} else {
				parts[i] = s.TabOff.Render(o)
			}
		}
		return strings.Join(parts, "")
	}
	gl := make([]string, len(groupBys))
	for i, g := range groupBys {
		gl[i] = g.label
	}
	per := m.currentPeriod()
	head := s.Title.Render("Informes") + "  " + seg(gl, m.group)
	sub := s.Faint.Render("periodo ") + s.Bold.Render(per.label) + s.Faint.Render("  ·  moneda ") + s.Bold.Render(string(m.currency))
	bodyH := height - 3

	if m.report == nil {
		msg := s.Faint.Render("Calculando…")
		if m.err != nil {
			msg = lipgloss.NewStyle().Foreground(s.Danger).Render("✗ " + m.err.Error())
		}
		return head + "\n" + sub + "\n\n" + center(msg, width, bodyH-1)
	}
	r := m.report

	// KPI cards.
	var events int
	var toks api.Tokens
	for _, row := range r.Rows {
		events += row.Events
		toks = addTokens(toks, row.Tokens)
	}
	kpi := func(label, value string, c interface {
		RGBA() (uint32, uint32, uint32, uint32)
	},
	) string {
		return s.Card.Width(max(18, (width-3)/4)).Render(s.Faint.Render(label) + "\n" + lipgloss.NewStyle().Foreground(c).Bold(true).Render(value))
	}
	unpriced := 0
	if r.UnpricedEvents != nil {
		unpriced = *r.UnpricedEvents
	}
	uc := s.Success
	if unpriced > 0 {
		uc = s.Warning
	}
	cards := lipgloss.JoinHorizontal(lipgloss.Top,
		kpi("total", m.fmtCost(r.Total), s.Primary), " ",
		kpi("peticiones", fmt.Sprintf("%d", events), s.Secondary), " ",
		kpi("tokens", humanTokens(totalTokens(toks)), s.Info), " ",
		kpi("sin precio", fmt.Sprintf("%d", unpriced), uc))

	if len(r.Rows) == 0 {
		return head + "\n" + sub + "\n" + cards + "\n" + center(s.Faint.Render("Sin gasto en este periodo"), width, bodyH-lipgloss.Height(cards))
	}

	// Bar chart rows.
	labelW := min(32, max(14, width/4))
	valW, shareW, evW := 11, 6, 10
	barW := max(6, width-labelW-valW-shareW-evW-6)
	var maxCost float64
	for _, row := range r.Rows {
		maxCost = max(maxCost, row.Cost)
	}
	listH := bodyH - lipgloss.Height(cards) - 2
	start, end := window(len(r.Rows), m.cursor, listH)
	var b strings.Builder
	b.WriteString(s.Faint.Render(pad(groupBys[m.group].label, labelW)+" "+pad("", barW)+" "+padLeft("coste", valW)+" "+padLeft("%", shareW)+" "+padLeft("peticiones", evW)) + "\n")
	for i := start; i < end; i++ {
		row := r.Rows[i]
		label := row.Label
		switch r.GroupBy {
		case api.GroupByRepo:
			label = shortRepo(label, m.sh.org)
		case api.GroupByModel:
			label = shortModel(label)
		}
		share := 0.0
		if r.Total > 0 {
			share = row.Cost / r.Total * 100
		}
		line := pad(label, labelW) + " " + pad(bar(row.Cost, maxCost, barW, s.Gradient), barW) + " " +
			padLeft(s.Bold.Render(m.fmtCost(row.Cost)), valW) + " " + padLeft(s.Faint.Render(fmt.Sprintf("%.0f%%", share)), shareW) + " " +
			padLeft(s.Faint.Render(fmt.Sprintf("%d", row.Events)), evW)
		if i == m.cursor {
			line = lipgloss.NewStyle().Foreground(s.Primary).Render("❯ ") + line
		} else {
			line = "  " + line
		}
		b.WriteString(line)
		if i < end-1 {
			b.WriteByte('\n')
		}
	}
	footer := ""
	if unpriced > 0 {
		footer = "\n" + lipgloss.NewStyle().Foreground(s.Warning).Render(fmt.Sprintf("⚠ %d peticiones usan la estimación del harness: falta su precio en la tabla (pídelo a un admin)", unpriced))
	}
	return head + "\n" + sub + "\n" + cards + "\n" + fitHeight(b.String(), listH+1) + footer
}

func addTokens(a, b api.Tokens) api.Tokens {
	return api.Tokens{
		Input: a.Input + b.Input, Output: a.Output + b.Output, Reasoning: a.Reasoning + b.Reasoning,
		CacheRead: a.CacheRead + b.CacheRead, CacheWrite: a.CacheWrite + b.CacheWrite,
	}
}

func apiTokens(t domain.Tokens) api.Tokens {
	return api.Tokens{Input: t.Input, Output: t.Output, Reasoning: t.Reasoning, CacheRead: t.CacheRead, CacheWrite: t.CacheWrite}
}
