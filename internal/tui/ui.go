package tui

import (
	"fmt"
	"image/color"
	"math"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/domain"
	"github.com/hiberus-industria/forge/internal/tui/theme"
)

// money formats USD micros in the preferred currency. When EUR is preferred
// but no rate is known yet, it falls back to USD.
func money(micros int64, cur domain.Currency, usdPerEUR float64) string {
	m := domain.Micros(micros)
	if cur == domain.CurrencyEUR && usdPerEUR > 0 {
		return domain.FormatAmount(m.USD()/usdPerEUR, domain.CurrencyEUR)
	}
	return domain.FormatAmount(m.USD(), domain.CurrencyUSD)
}

// moneyAlt formats the amount in the other currency (for secondary display).
func moneyAlt(micros int64, cur domain.Currency, usdPerEUR float64) string {
	if usdPerEUR <= 0 {
		return ""
	}
	if cur == domain.CurrencyEUR {
		return domain.FormatAmount(domain.Micros(micros).USD(), domain.CurrencyUSD)
	}
	return domain.FormatAmount(domain.Micros(micros).USD()/usdPerEUR, domain.CurrencyEUR)
}

// humanTokens renders 1234567 as 1.2M.
func humanTokens(n int64) string {
	f := float64(n)
	switch {
	case f >= 1e9:
		return fmt.Sprintf("%.1fB", f/1e9)
	case f >= 1e6:
		return fmt.Sprintf("%.1fM", f/1e6)
	case f >= 1e3:
		return fmt.Sprintf("%.1fk", f/1e3)
	}
	return fmt.Sprintf("%d", n)
}

func totalTokens(t api.Tokens) int64 {
	return t.Input + t.Output + t.Reasoning + t.CacheRead + t.CacheWrite
}

// ago renders a compact relative time in Spanish.
func ago(t time.Time, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < 0:
		return "ahora"
	case d < time.Minute:
		return "hace un momento"
	case d < time.Hour:
		return fmt.Sprintf("hace %d min", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("hace %d h", int(d.Hours()))
	case d < 48*time.Hour:
		return "ayer"
	}
	return fmt.Sprintf("hace %d d", int(d.Hours()/24))
}

// trunc cuts a (possibly styled) string to w cells.
func trunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

// pad right-pads a (possibly styled) string to w cells, truncating if needed.
func pad(s string, w int) string {
	s = trunc(s, w)
	if g := w - lipgloss.Width(s); g > 0 {
		s += strings.Repeat(" ", g)
	}
	return s
}

// padLeft left-pads to w cells.
func padLeft(s string, w int) string {
	s = trunc(s, w)
	if g := w - lipgloss.Width(s); g > 0 {
		s = strings.Repeat(" ", g) + s
	}
	return s
}

// spread lays left and right parts on one line of width w.
func spread(left, right string, w int) string {
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return trunc(left, w-lipgloss.Width(right)-1) + " " + right
	}
	return left + strings.Repeat(" ", gap) + right
}

// bar renders a horizontal bar of value/maxV over width cells using eighth
// blocks for sub-cell precision, coloured along the gradient.
func bar(value, maxV float64, width int, cols []color.Color) string {
	if width <= 0 || maxV <= 0 || value <= 0 {
		return ""
	}
	cells := value / maxV * float64(width)
	full := int(cells)
	frac := cells - float64(full)
	eighths := []string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"}
	grad := lipgloss.Blend1D(max(width, 2), cols...)
	var b strings.Builder
	for i := 0; i < full && i < width; i++ {
		b.WriteString(lipgloss.NewStyle().Foreground(grad[i]).Render("█"))
	}
	if full < width {
		if e := eighths[int(frac*8)]; e != "" {
			b.WriteString(lipgloss.NewStyle().Foreground(grad[full]).Render(e))
		} else if full == 0 {
			b.WriteString(lipgloss.NewStyle().Foreground(grad[0]).Render("▏"))
		}
	}
	return b.String()
}

// sparkline renders values with ▁▂▃▄▅▆▇█, scaled to the maximum.
func sparkline(values []float64, c color.Color) string {
	ticks := []rune("▁▂▃▄▅▆▇█")
	maxV := 0.0
	for _, v := range values {
		maxV = math.Max(maxV, v)
	}
	on := lipgloss.NewStyle().Foreground(c)
	off := lipgloss.NewStyle().Faint(true)
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

// statePill renders the lifecycle state badge.
func statePill(s *theme.Styles, st api.TaskState) string {
	switch st {
	case api.InProgress:
		return s.Pill("EN CURSO", s.Info)
	case api.InReview:
		return s.Pill("REVISIÓN", s.Secondary)
	case api.Done:
		return s.Pill("HECHO", s.Success)
	default:
		return s.Pill("PENDIENTE", s.Muted)
	}
}

func harnessIcon(s *theme.Styles, h string) string {
	switch h {
	case "claude-code":
		return lipgloss.NewStyle().Foreground(s.Accent).Render("✻")
	case "opencode":
		return lipgloss.NewStyle().Foreground(s.Info).Render("◇")
	}
	return s.Faint.Render("•")
}

func harnessName(h string) string {
	switch h {
	case "claude-code":
		return "Claude Code"
	case "opencode":
		return "OpenCode"
	}
	return h
}

// shortRepo drops the organisation prefix when it is the tracked org.
func shortRepo(full, org string) string {
	if o, n, ok := strings.Cut(full, "/"); ok && strings.EqualFold(o, org) {
		return n
	}
	return full
}

// shortModel trims vendor prefixes from model ids for compact display.
func shortModel(m string) string {
	m = domain.NormalizeModel(m)
	return strings.TrimPrefix(m, "claude-")
}

func issueRef(r api.IssueRef) (domain.IssueRef, error) {
	return domain.ParseIssueRef(fmt.Sprintf("%s#%d", r.Repo, r.Number))
}

// center places content in the middle of a w×h area.
func center(content string, w, h int) string {
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, content)
}

// fitHeight pads or cuts content to exactly h lines.
func fitHeight(content string, h int) string {
	lines := strings.Split(content, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// window returns the [start,end) slice of n items so that cursor is visible
// in a viewport of h rows.
func window(n, cursor, h int) (int, int) {
	if h <= 0 || n == 0 {
		return 0, 0
	}
	if n <= h {
		return 0, n
	}
	start := cursor - h/2
	start = max(0, min(start, n-h))
	return start, start + h
}
