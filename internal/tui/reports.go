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

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/domain"
)

var reportKeys = struct {
	Group, Period, Custom, Currency, Export, Drill, Back, Refresh key.Binding
}{
	Group:    key.NewBinding(key.WithKeys("g", "G"), key.WithHelp("g", "agrupar por")),
	Period:   key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "periodo")),
	Custom:   key.NewBinding(key.WithKeys("P"), key.WithHelp("P", "elegir fechas")),
	Currency: key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "euros / dólares")),
	Export:   key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "exportar CSV")),
	Drill:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "ver sus tareas")),
	Back:     key.NewBinding(key.WithKeys("esc", "backspace"), key.WithHelp("esc", "volver")),
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
	{api.GroupByHarness, "herramienta"},
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
		{"7 días", day.AddDate(0, 0, -6), tomorrow},
		{"30 días", day.AddDate(0, 0, -29), tomorrow},
		{"este mes", month, month.AddDate(0, 1, 0)},
		{"mes pasado", month.AddDate(0, -1, 0), month},
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
	return period{label: "a medida", from: from, to: to.AddDate(0, 0, 1)}, nil
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
	// repo narrows the report to one repository (drill-down from a repo
	// row); empty = everything.
	repo      string
	prevGroup int
	report    *api.CostReport
	loading   bool
	err       error
	seq       int

	list          rowList[api.CostRow]
	width, height int
}

func newReportsScreen(sh *shared) *reportsScreen {
	return &reportsScreen{sh: sh, currency: sh.currency, loading: true,
		list: newRowList(sh.st, func(r api.CostRow) string { return r.Label })}
}

func (m *reportsScreen) canDrill() bool {
	return m.repo == "" && groupBys[m.group].g == api.GroupByRepo
}

func (m *reportsScreen) Hints() []key.Binding {
	hs := []key.Binding{reportKeys.Group, reportKeys.Period}
	if m.canDrill() {
		hs = append([]key.Binding{reportKeys.Drill}, hs...)
	}
	if m.repo != "" {
		hs = append([]key.Binding{reportKeys.Back}, hs...)
	}
	return append(hs, reportKeys.Export)
}

func (m *reportsScreen) Help() []helpGroup {
	return []helpGroup{
		{"Moverse", []key.Binding{navKeys.Up, navKeys.Down, navKeys.PgUp, navKeys.PgDown, reportKeys.Drill, reportKeys.Back}},
		{"Informe", []key.Binding{reportKeys.Group, reportKeys.Period, reportKeys.Custom, reportKeys.Currency}},
		{"Datos", []key.Binding{reportKeys.Export, reportKeys.Refresh}},
	}
}

func (m *reportsScreen) Capturing() bool { return false }
func (m *reportsScreen) Init() tea.Cmd   { return m.load() }

// Body rows above the list: two selectors, gap, totals, gap, column titles.
const reportChrome = 6

func (m *reportsScreen) SetSize(w, h int) {
	m.width, m.height = w, h
	m.list.SetHeight(h - reportChrome - m.footerHeight())
}

func (m *reportsScreen) unpriced() int {
	if m.report == nil || m.report.UnpricedEvents == nil {
		return 0
	}
	return *m.report.UnpricedEvents
}

func (m *reportsScreen) footerHeight() int {
	if m.unpriced() > 0 {
		return 2
	}
	return 0
}

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
	r := api.GetCostReportParams{GroupBy: groupBys[m.group].g, Currency: &cur, From: &from, To: &to}
	if m.repo != "" {
		repo := m.repo
		r.Repo = &repo
	}
	return r
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
			m.list.SetItems(r.Rows, func(c api.CostRow) string { return c.Key })
			m.list.SetHeight(m.height - reportChrome - m.footerHeight())
		}
	case customPeriodMsg:
		m.period, m.custom = -1, msg.p
		return m, m.load()
	case refreshMsg:
		return m, m.load()
	case tea.MouseWheelMsg:
		m.list.Wheel(msg)
	case tea.MouseClickMsg:
		m.list.Click(msg.Y - reportChrome)
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, reportKeys.Group):
			d := 1
			if msg.String() == "G" {
				d = len(groupBys) - 1
			}
			m.group = (m.group + d) % len(groupBys)
			m.list.Top()
			return m, m.load()
		case key.Matches(msg, reportKeys.Period):
			m.period = (m.period + 1) % len(presetPeriods(m.sh.now()))
			return m, m.load()
		case key.Matches(msg, reportKeys.Custom):
			loc := m.sh.now().Location()
			return m, openModal(newInputModal(m.sh.st, "Elegir fechas", "Desde y hasta, ambos incluidos, separados por dos puntos (..).", "2026-09-01..2026-09-30",
				func(v string) error { _, err := parseCustomPeriod(v, loc); return err },
				func(v string) tea.Cmd {
					p, _ := parseCustomPeriod(v, loc)
					return func() tea.Msg { return customPeriodMsg{p} }
				}))
		case key.Matches(msg, reportKeys.Currency):
			if m.currency == domain.CurrencyEUR {
				m.currency = domain.CurrencyUSD
			} else {
				m.currency = domain.CurrencyEUR
			}
			return m, m.load()
		case key.Matches(msg, reportKeys.Export):
			if m.report == nil {
				return m, nil
			}
			path, err := writeReportCSV(m.sh.exportDir, *m.report, m.currentPeriod())
			if err != nil {
				return m, toast(toastErr, "No se pudo exportar: "+err.Error())
			}
			return m, toast(toastOK, "CSV guardado en "+path)
		case key.Matches(msg, reportKeys.Drill):
			r, ok := m.list.Selected()
			if !ok || !m.canDrill() || r.Key == "-" || r.Key == "" {
				return m, nil
			}
			m.repo, m.prevGroup = r.Key, m.group
			m.group = 1 // tarea
			m.list.Top()
			return m, m.load()
		case key.Matches(msg, reportKeys.Back):
			if m.repo == "" {
				return m, nil
			}
			m.repo, m.group = "", m.prevGroup
			return m, m.load()
		case key.Matches(msg, reportKeys.Refresh):
			return m, m.load()
		}
		m.list.Nav(msg)
	}
	return m, nil
}

func (m *reportsScreen) fmtCost(v float64) string {
	return domain.FormatAmount(v, domain.Currency(m.currency))
}

// rowLabel names a report row for humans.
func (m *reportsScreen) rowLabel(r api.CostRow) string {
	switch m.report.GroupBy {
	case api.GroupByRepo:
		if r.Key == "-" {
			return "sin repositorio"
		}
		return shortRepo(r.Label, m.sh.org)
	case api.GroupByModel:
		return shortModel(r.Label)
	case api.GroupByIssue:
		if r.Key == "-" {
			return "sin tarea (bandeja)"
		}
		ref := r.Label
		if repo, n, ok := strings.Cut(r.Label, "#"); ok {
			ref = shortRepo(repo, m.sh.org) + "#" + n
		}
		if t := m.sh.title(r.Key); t != "" {
			return ref + "  " + t
		}
		return ref
	case api.GroupByDay:
		if d, err := time.Parse("2006-01-02", r.Key); err == nil {
			return shortDate(d)
		}
	}
	return r.Label
}

func (m *reportsScreen) View() string {
	s := m.sh.st
	width, height := m.width, m.height
	const labelW = 10

	groups := make([]string, len(groupBys))
	for i, g := range groupBys {
		groups[i] = g.label
	}
	line1 := s.Faint.Render(pad("Coste por", labelW)) + segmented(s, groups, m.group)
	if m.repo != "" {
		line1 = s.Faint.Render(pad("Coste por", labelW)) + s.Hi.Render(groupBys[m.group].label) +
			s.Faint.Render(" en ") + s.Bold.Render(shortRepo(m.repo, m.sh.org)) + "   " + hint(s, "esc", "volver a todos")
	}
	periods := presetPeriods(m.sh.now())
	pl := make([]string, 0, len(periods)+1)
	for _, p := range periods {
		pl = append(pl, p.label)
	}
	pi := m.period
	if pi < 0 {
		pl = append(pl, "a medida")
		pi = len(pl) - 1
	}
	cur := 0
	if m.currency == domain.CurrencyUSD {
		cur = 1
	}
	line2 := spread(s.Faint.Render(pad("Periodo", labelW))+segmented(s, pl, pi), segmented(s, []string{"EUR", "USD"}, cur), width)
	sel := line1 + "\n" + line2
	bodyH := max(1, height-3)

	if m.report == nil {
		if m.err != nil {
			return sel + "\n\n" + emptyState(s, width, bodyH, s.Err.Render("No se pudo calcular el informe"), m.err.Error(), "Pulsa r para reintentar.")
		}
		return sel + "\n\n" + center(s.Faint.Render("Calculando…"), width, bodyH)
	}
	r := m.report

	var events int
	var toks api.Tokens
	for _, row := range r.Rows {
		events += row.Events
		toks = addTokens(toks, row.Tokens)
	}
	p := m.currentPeriod()
	totals := s.Hi.Render(m.fmtCost(r.Total)) + s.Faint.Render(fmt.Sprintf("  en total  ·  %s  ·  %s tokens  ·  %s – %s",
		plural(events, "petición", "peticiones"), humanTokens(totalTokens(toks)), dayMonth(p.from), dayMonth(p.to.AddDate(0, 0, -1))))
	if m.loading {
		totals += s.Dim.Render("  ·  actualizando…")
	}
	head := sel + "\n\n" + trunc(totals, width) + "\n\n"
	if len(r.Rows) == 0 {
		return head + emptyState(s, width, max(1, height-reportChrome+1), "No hay gasto en este periodo", "Pulsa p para cambiar el periodo o P para elegir fechas.")
	}

	lw := 20
	if r.GroupBy == api.GroupByIssue {
		lw = min(44, width*2/5)
	}
	var top float64
	for _, row := range r.Rows {
		top = max(top, row.Cost)
	}
	cols := func(cr *api.CostRow, sel bool, w int) []col {
		if cr == nil {
			return []col{{text: groupBys[m.group].label, w: lw}, {text: ""}, {text: "coste", w: 10, right: true},
				{text: "%", w: 4, right: true}, {text: "pet.", w: 6, right: true}}
		}
		share := 0.0
		if r.Total > 0 {
			share = cr.Cost / r.Total * 100
		}
		labelSt := s.Base
		if sel {
			labelSt = s.Bold
		}
		barW := max(4, w-2-lw-10-4-6-colGap*4)
		frac := 0.0
		if top > 0 {
			frac = cr.Cost / top
		}
		bar := meter(s, frac, barW, s.Primary)
		if sel {
			bar = lipgloss.NewStyle().Background(s.Surface).Render(bar)
		}
		return []col{
			{text: m.rowLabel(*cr), w: lw, st: labelSt},
			{text: bar, raw: true},
			{text: m.fmtCost(cr.Cost), w: 10, right: true, st: s.Base},
			{text: fmt.Sprintf("%.0f%%", share), w: 4, right: true, st: s.Faint},
			{text: fmt.Sprint(cr.Events), w: 6, right: true, st: s.Faint},
		}
	}
	out := head + headerRow(s, width, cols(nil, false, width)...) + "\n" +
		m.list.View(width, func(cr api.CostRow, sel bool, w int) string { return row(s, w, sel, cols(&cr, sel, w)...) })
	if n := m.unpriced(); n > 0 {
		out = fitHeight(out, height-2) + "\n\n" + s.Warn.Render("▲ ") +
			s.Faint.Render(trunc(fmt.Sprintf("%s sin precio conocido: su coste es la estimación del harness. Pide a un admin que añada el modelo.", plural(n, "petición", "peticiones")), width-2))
	}
	return out
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
