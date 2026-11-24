package tui

import (
	"fmt"
	"image/color"
	"math"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/hiberus-industria/forge/internal/tui/theme"
)

// ---------------------------------------------------------------- rows

// col is one cell of a row. w = 0 makes it flexible: it takes the width the
// fixed columns leave. Text is plain unless raw is set (already styled).
type col struct {
	text  string
	w     int
	right bool
	st    lipgloss.Style
	raw   bool
}

// colGap is the space between columns, everywhere.
const colGap = 2

// row renders one list row: a 2-cell gutter (the cursor bar when selected)
// and the columns. A selected row gets the surface background edge to edge,
// so every piece is styled with it.
func row(s *theme.Styles, width int, sel bool, cols ...col) string {
	bg := func(st lipgloss.Style) lipgloss.Style {
		if sel {
			return st.Background(s.Surface)
		}
		return st
	}
	space := bg(lipgloss.NewStyle())
	inner := width - 2
	fixed, flex := 0, -1
	for i, c := range cols {
		if c.w == 0 && flex < 0 {
			flex = i
			continue
		}
		fixed += c.w
	}
	fixed += colGap * max(0, len(cols)-1)

	var b strings.Builder
	if sel {
		b.WriteString(bg(s.Cursor).Render("▌") + space.Render(" "))
	} else {
		b.WriteString("  ")
	}
	used := 0
	for i, c := range cols {
		w := c.w
		if i == flex {
			w = max(0, inner-fixed)
		}
		if w == 0 {
			continue
		}
		if used > 0 {
			b.WriteString(space.Render(strings.Repeat(" ", colGap)))
			used += colGap
		}
		w = min(w, inner-used)
		if w <= 0 {
			break
		}
		txt := c.text
		if !c.raw {
			txt = bg(c.st).Render(trunc(txt, w))
		} else {
			txt = trunc(txt, w)
		}
		gap := space.Render(strings.Repeat(" ", max(0, w-lipgloss.Width(txt))))
		if c.right {
			b.WriteString(gap + txt)
		} else {
			b.WriteString(txt + gap)
		}
		used += w
	}
	if rest := inner - used; rest > 0 {
		b.WriteString(space.Render(strings.Repeat(" ", rest)))
	}
	return b.String()
}

// headerRow renders column titles aligned with row().
func headerRow(s *theme.Styles, width int, cols ...col) string {
	for i := range cols {
		cols[i].st = s.Dim
		cols[i].raw = false
	}
	return row(s, width, false, cols...)
}

// ---------------------------------------------------------------- list

// rowList is a scrolling, filterable list. Unlike bubbles/list it scrolls
// line by line (no pages) and leaves rendering to the screen, so tasks,
// inbox, feed and reports look and move the same way.
type rowList[T any] struct {
	items     []T
	vis       []int
	cursor    int
	offset    int
	height    int
	text      func(T) string
	input     textinput.Model
	filtering bool
	query     string
}

func newRowList[T any](s *theme.Styles, text func(T) string) rowList[T] {
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "escribe para filtrar"
	ti.SetStyles(inputStyles(s))
	return rowList[T]{text: text, input: ti, height: 1}
}

func (l *rowList[T]) restyle(s *theme.Styles) { l.input.SetStyles(inputStyles(s)) }

// SetItems replaces the items, keeping the cursor on the same item (by key)
// when it is still there.
func (l *rowList[T]) SetItems(items []T, keyOf func(T) string) {
	var cur string
	if it, ok := l.Selected(); ok && keyOf != nil {
		cur = keyOf(it)
	}
	l.items = items
	l.refilter()
	if cur != "" {
		for i, idx := range l.vis {
			if keyOf(l.items[idx]) == cur {
				l.cursor = i
				break
			}
		}
	}
	l.clamp()
}

func (l *rowList[T]) refilter() {
	terms := l.Terms()
	l.vis = l.vis[:0]
	for i, it := range l.items {
		if len(terms) == 0 || matchAll(l.text(it), terms) {
			l.vis = append(l.vis, i)
		}
	}
	l.clamp()
}

func matchAll(text string, terms []string) bool {
	low := strings.ToLower(text)
	for _, t := range terms {
		if !strings.Contains(low, t) {
			return false
		}
	}
	return true
}

// Terms are the lowercase words of the filter ("#" ignored).
func (l *rowList[T]) Terms() []string {
	return strings.Fields(strings.ToLower(strings.ReplaceAll(l.query, "#", " ")))
}

func (l *rowList[T]) Len() int   { return len(l.vis) }
func (l *rowList[T]) Total() int { return len(l.items) }
func (l *rowList[T]) Cursor() int {
	return l.cursor
}

func (l *rowList[T]) Selected() (T, bool) {
	var zero T
	if l.cursor < 0 || l.cursor >= len(l.vis) {
		return zero, false
	}
	return l.items[l.vis[l.cursor]], true
}

func (l *rowList[T]) Visible() []T {
	out := make([]T, len(l.vis))
	for i, idx := range l.vis {
		out[i] = l.items[idx]
	}
	return out
}

func (l *rowList[T]) SetHeight(h int) {
	l.height = max(1, h)
	l.clamp()
}

func (l *rowList[T]) Move(d int) {
	l.cursor += d
	l.clamp()
}

func (l *rowList[T]) Top() { l.cursor = 0; l.clamp() }

// clamp keeps the cursor in range and visible.
func (l *rowList[T]) clamp() {
	n := len(l.vis)
	l.cursor = max(0, min(l.cursor, n-1))
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+l.height {
		l.offset = l.cursor - l.height + 1
	}
	l.offset = max(0, min(l.offset, n-l.height))
}

// Click selects the row at y (relative to the first row). It reports
// whether a row was hit.
func (l *rowList[T]) Click(y int) bool {
	i := l.offset + y
	if y < 0 || y >= l.height || i >= len(l.vis) {
		return false
	}
	l.cursor = i
	return true
}

var navKeys = struct{ UpDown, Up, Down, PgUp, PgDown, Home, End key.Binding }{
	UpDown: key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "moverse")),
	Up:     key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑ k", "subir")),
	Down:   key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓ j", "bajar")),
	PgUp:   key.NewBinding(key.WithKeys("pgup", "ctrl+u"), key.WithHelp("pgup", "página arriba")),
	PgDown: key.NewBinding(key.WithKeys("pgdown", "ctrl+d"), key.WithHelp("pgdn", "página abajo")),
	Home:   key.NewBinding(key.WithKeys("home"), key.WithHelp("inicio", "primero")),
	End:    key.NewBinding(key.WithKeys("end"), key.WithHelp("fin", "último")),
}

// Nav handles cursor movement keys and reports whether k was one.
func (l *rowList[T]) Nav(k tea.KeyPressMsg) bool {
	switch {
	case key.Matches(k, navKeys.Up):
		l.Move(-1)
	case key.Matches(k, navKeys.Down):
		l.Move(1)
	case key.Matches(k, navKeys.PgUp):
		l.Move(-l.height)
	case key.Matches(k, navKeys.PgDown):
		l.Move(l.height)
	case key.Matches(k, navKeys.Home):
		l.cursor = 0
		l.clamp()
	case key.Matches(k, navKeys.End):
		l.cursor = len(l.vis) - 1
		l.clamp()
	default:
		return false
	}
	return true
}

// Wheel moves the cursor with the mouse wheel.
func (l *rowList[T]) Wheel(m tea.MouseWheelMsg) {
	switch m.Button {
	case tea.MouseWheelUp:
		l.Move(-1)
	case tea.MouseWheelDown:
		l.Move(1)
	}
}

// StartFilter focuses the filter input (filtering as you type).
func (l *rowList[T]) StartFilter() tea.Cmd {
	l.filtering = true
	l.input.SetValue(l.query)
	l.input.CursorEnd()
	return l.input.Focus()
}

func (l *rowList[T]) Filtering() bool { return l.filtering }
func (l *rowList[T]) Query() string   { return l.query }

// ClearFilter drops the filter.
func (l *rowList[T]) ClearFilter() {
	l.query, l.filtering = "", false
	l.input.Blur()
	l.refilter()
}

// FilterKey handles a key while filtering: enter keeps the filter, esc
// drops it, arrows move the cursor, the rest is typed.
func (l *rowList[T]) FilterKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		l.ClearFilter()
		return nil
	case "enter":
		l.filtering = false
		l.input.Blur()
		return nil
	case "up", "down":
		l.Nav(k)
		return nil
	}
	var cmd tea.Cmd
	l.input, cmd = l.input.Update(k)
	if v := l.input.Value(); v != l.query {
		l.query = v
		l.cursor = 0
		l.refilter()
	}
	return cmd
}

// FilterView is the filter line: the input while editing, or the applied
// filter with how to drop it.
func (l *rowList[T]) FilterView(s *theme.Styles, width int) string {
	count := s.Faint.Render(fmt.Sprintf("%d de %d", len(l.vis), len(l.items)))
	if l.filtering {
		l.input.SetWidth(max(10, width-lipgloss.Width(count)-6))
		return spread(l.input.View(), count, width)
	}
	return spread(s.Hi.Render("/ ")+s.Base.Render(l.query)+"  "+hint(s, "esc", "quitar filtro"), count, width)
}

// View renders the visible window of rows, with a scrollbar on the right
// edge when the list does not fit.
func (l *rowList[T]) View(width int, render func(it T, sel bool, width int) string) string {
	end := min(len(l.vis), l.offset+l.height)
	bar := len(l.vis) > l.height
	rw := width
	if bar {
		rw = width - 2
	}
	lines := make([]string, 0, l.height)
	for i := l.offset; i < end; i++ {
		lines = append(lines, render(l.items[l.vis[i]], i == l.cursor, rw))
	}
	for len(lines) < l.height {
		lines = append(lines, "")
	}
	if !bar {
		return strings.Join(lines, "\n")
	}
	thumbH := max(1, l.height*l.height/len(l.vis))
	thumbY := 0
	if n := len(l.vis) - l.height; n > 0 {
		thumbY = (l.height - thumbH) * l.offset / n
	}
	for i := range lines {
		mark := "│"
		st := lipgloss.NewStyle().Foreground(currentStyles.Border)
		if i >= thumbY && i < thumbY+thumbH {
			mark = "┃"
			st = lipgloss.NewStyle().Foreground(currentStyles.Muted)
		}
		lines[i] = pad(lines[i], rw) + " " + st.Render(mark)
	}
	return strings.Join(lines, "\n")
}

// currentStyles is the palette for components that have no access to the
// shared state (the scrollbar). The root keeps it in sync.
var currentStyles = theme.Default().Styles(true)

// mark underlines the filter terms inside text, keeping the base style.
func mark(text string, terms []string, base lipgloss.Style) string {
	if len(terms) == 0 {
		return base.Render(text)
	}
	low := strings.ToLower(text)
	var idx []int
	for _, t := range terms {
		from := 0
		for {
			at := strings.Index(low[from:], t)
			if at < 0 {
				break
			}
			at += from
			start := utf8.RuneCountInString(low[:at])
			for r := range utf8.RuneCountInString(t) {
				idx = append(idx, start+r)
			}
			from = at + len(t)
		}
	}
	if len(idx) == 0 {
		return base.Render(text)
	}
	return lipgloss.StyleRunes(text, idx, base.Underline(true).Foreground(currentStyles.Primary), base)
}

// ---------------------------------------------------------------- bits

func inputStyles(s *theme.Styles) textinput.Styles {
	st := textinput.DefaultStyles(s.IsDark)
	st.Focused.Prompt = s.Hi
	st.Blurred.Prompt = s.Faint
	st.Focused.Text = s.Base
	st.Blurred.Text = s.Base
	st.Focused.Placeholder = s.Dim
	st.Blurred.Placeholder = s.Dim
	st.Cursor.Color = s.Primary
	return st
}

// hint renders "key description".
func hint(s *theme.Styles, k, desc string) string {
	return s.Key.Render(k) + " " + s.Faint.Render(desc)
}

// hints renders the footer from bindings, dropping the ones that do not
// fit instead of cutting a word in half.
func hints(s *theme.Styles, width int, bs ...key.Binding) string {
	var parts []string
	used := 0
	for _, b := range bs {
		if !b.Enabled() {
			continue
		}
		h := hint(s, b.Help().Key, b.Help().Desc)
		w := lipgloss.Width(h)
		if used > 0 {
			w += 3
		}
		if used+w > width {
			break
		}
		parts = append(parts, h)
		used += w
	}
	return strings.Join(parts, "   ")
}

// segmented renders the options of a setting with the active one marked,
// so it is clear what the key cycles through.
func segmented(s *theme.Styles, opts []string, active int) string {
	out := make([]string, len(opts))
	for i, o := range opts {
		if i == active {
			out[i] = s.Hi.Render(o)
		} else {
			out[i] = s.Dim.Render(o)
		}
	}
	return strings.Join(out, s.Dim.Render(" · "))
}

// link renders label as an OSC 8 hyperlink to url. The label never wraps,
// so ctrl+click always opens the whole URL.
func link(s *theme.Styles, url, label string) string {
	if url == "" {
		return s.Base.Render(label)
	}
	return s.Link.Hyperlink(url).Render(label)
}

// meter is a thin horizontal bar (━) for frac of width cells, with half-cell
// precision, over a track in the border colour.
func meter(s *theme.Styles, frac float64, width int, c color.Color) string {
	if width <= 0 {
		return ""
	}
	frac = math.Max(0, math.Min(1, frac))
	halves := int(math.Round(frac * float64(width) * 2))
	if frac > 0 && halves == 0 {
		halves = 1
	}
	full := halves / 2
	on := lipgloss.NewStyle().Foreground(c)
	out := on.Render(strings.Repeat("━", full))
	n := full
	if halves%2 == 1 && n < width {
		out += on.Render("╸")
		n++
	}
	return out + s.Rule.Render(strings.Repeat("━", width-n))
}

// stacked renders shares of a whole as one bar of coloured segments.
func stacked(values []float64, colors []color.Color, width int) string {
	var total float64
	for _, v := range values {
		total += v
	}
	if total <= 0 || width <= 0 {
		return ""
	}
	var b strings.Builder
	used := 0
	for i, v := range values {
		n := int(math.Round(v / total * float64(width)))
		if v > 0 && n == 0 {
			n = 1
		}
		if i == len(values)-1 || used+n > width {
			n = width - used
		}
		if n <= 0 {
			continue
		}
		b.WriteString(lipgloss.NewStyle().Foreground(colors[i%len(colors)]).Render(strings.Repeat("━", n)))
		used += n
	}
	return b.String()
}

// sparkline renders values with ▁▂▃▄▅▆▇█, scaled to the maximum.
func sparkline(values []float64, c, offC color.Color) string {
	ticks := []rune("▁▂▃▄▅▆▇█")
	maxV := 0.0
	for _, v := range values {
		maxV = math.Max(maxV, v)
	}
	on := lipgloss.NewStyle().Foreground(c)
	off := lipgloss.NewStyle().Foreground(offC)
	var b strings.Builder
	for _, v := range values {
		if maxV <= 0 || v <= 0 {
			b.WriteString(off.Render("▁"))
			continue
		}
		i := max(1, int(math.Round(v/maxV*float64(len(ticks)-1))))
		b.WriteString(on.Render(string(ticks[i])))
	}
	return b.String()
}

// emptyState renders a centred title plus hint lines.
func emptyState(s *theme.Styles, w, h int, title string, lines ...string) string {
	out := []string{s.Bold.Render(title)}
	if len(lines) > 0 {
		out = append(out, "")
		for _, l := range lines {
			out = append(out, s.Faint.Render(l))
		}
	}
	return center(lipgloss.JoinVertical(lipgloss.Center, out...), w, h)
}

// plural picks the singular or plural noun for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
