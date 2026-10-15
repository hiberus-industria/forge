package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
)

// Provisional palette, aligned with the TUI theme but independent of it.
var (
	isDark = lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
	ld     = lipgloss.LightDark(isDark)

	cAccent  = ld(lipgloss.Color("#5B3FD9"), lipgloss.Color("#9D85FF"))
	cAccent2 = ld(lipgloss.Color("#C2378F"), lipgloss.Color("#FF7AC6"))
	cOK      = ld(lipgloss.Color("#1A8F55"), lipgloss.Color("#3DD68C"))
	cWarn    = ld(lipgloss.Color("#B26B00"), lipgloss.Color("#F5B544"))
	cErr     = ld(lipgloss.Color("#C62F35"), lipgloss.Color("#FF6369"))
	cMuted   = ld(lipgloss.Color("#6B6880"), lipgloss.Color("#9A97AD"))
	cText    = ld(lipgloss.Color("#1F1D2B"), lipgloss.Color("#ECEBF3"))
)

var (
	sTitle  = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sAccent = lipgloss.NewStyle().Foreground(cAccent2)
	sBold   = lipgloss.NewStyle().Bold(true).Foreground(cText)
	sMuted  = lipgloss.NewStyle().Foreground(cMuted)
	sOK     = lipgloss.NewStyle().Foreground(cOK)
	sWarn   = lipgloss.NewStyle().Foreground(cWarn)
	sErr    = lipgloss.NewStyle().Foreground(cErr)
	sKey    = lipgloss.NewStyle().Foreground(cMuted).Width(16)
	sCode   = lipgloss.NewStyle().Foreground(cAccent2).Bold(true)
	sBox    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(0, 2)
)

// out is the CLI writer; lipgloss downsamples colors to the terminal profile.
type out struct{ w io.Writer }

func (o out) println(s ...any)      { lipgloss.Fprintln(o.w, s...) }
func (o out) title(s string)        { o.println(sTitle.Render("⚒ " + s)) }
func (o out) ok(f string, a ...any) { o.println(sOK.Render("✓ ") + fmt.Sprintf(f, a...)) }

func (o out) warn(f string, a ...any) { o.println(sWarn.Render("! ") + fmt.Sprintf(f, a...)) }

func (o out) fail(f string, a ...any) { o.println(sErr.Render("✗ ") + fmt.Sprintf(f, a...)) }

func (o out) info(f string, a ...any) { o.println(sAccent.Render("› ") + fmt.Sprintf(f, a...)) }

func (o out) hint(f string, a ...any) { o.println(sMuted.Render("  " + fmt.Sprintf(f, a...))) }
func (o out) kv(k string, v any)      { o.println("  " + sKey.Render(k) + fmt.Sprint(v)) }
func (o out) box(lines ...string)     { o.println(sBox.Render(strings.Join(lines, "\n"))) }
