package tui

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
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

// padLines pads (or cuts) every line of a block to w cells.
func padLines(s string, w int) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = pad(l, w)
	}
	return strings.Join(lines, "\n")
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

// stateGlyph is the lifecycle state as one coloured glyph. Only work in
// progress gets the accent; closed work is green, the rest stays neutral.
func stateGlyph(s *theme.Styles, st api.TaskState) (string, lipgloss.Style) {
	switch st {
	case api.InProgress:
		return "●", s.Hi
	case api.InReview:
		return "◐", s.Base
	case api.Done:
		return "✓", s.OK
	default:
		return "○", s.Dim
	}
}

func stateName(st api.TaskState) string {
	switch st {
	case api.InProgress:
		return "En curso"
	case api.InReview:
		return "En revisión"
	case api.Done:
		return "Cerrada"
	}
	return "Pendiente"
}

// stateLabel renders glyph plus name.
func stateLabel(s *theme.Styles, st api.TaskState) string {
	g, gs := stateGlyph(s, st)
	return gs.Render(g) + " " + s.Faint.Render(stateName(st))
}

// openURL opens url in the default browser (WSL included).
func openURL(url string) tea.Cmd {
	return func() tea.Msg {
		var cmd *exec.Cmd
		switch {
		case runtime.GOOS == "darwin":
			cmd = exec.Command("open", url)
		case os.Getenv("WSL_DISTRO_NAME") != "":
			cmd = exec.Command("cmd.exe", "/c", "start", "", strings.ReplaceAll(url, "&", "^&"))
		default:
			cmd = exec.Command("xdg-open", url)
		}
		if err := cmd.Start(); err != nil {
			return toastMsg{kind: toastErr, text: "No se pudo abrir el navegador: " + err.Error()}
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
}

// copyText puts text in the clipboard (OSC 52) and confirms it.
func copyText(text, what string) tea.Cmd {
	return tea.Batch(tea.SetClipboard(text), toast(toastOK, what+" copiado al portapapeles"))
}

// shortDate renders a day as "mié 14 oct".
func shortDate(t time.Time) string {
	days := []string{"dom", "lun", "mar", "mié", "jue", "vie", "sáb"}
	months := []string{"ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sep", "oct", "nov", "dic"}
	return fmt.Sprintf("%s %d %s", days[t.Weekday()], t.Day(), months[t.Month()-1])
}

// dayMonth renders a day as "14 oct".
func dayMonth(t time.Time) string {
	months := []string{"ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sep", "oct", "nov", "dic"}
	return fmt.Sprintf("%d %s", t.Day(), months[t.Month()-1])
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
