// Package theme holds the TUI palette and the lipgloss styles built from it.
//
// The default palette is PROVISIONAL: a corporate-flavoured palette in the
// Charm style, pending the official brand colours. Every token can be
// overridden from a TOML file (config theme_file):
//
//	[dark]
//	primary = "#00A19B"
//	[light]
//	primary = "#007C77"
package theme

import (
	"fmt"
	"image/color"
	"os"
	"reflect"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/pelletier/go-toml/v2"
)

// Palette is a set of colour tokens (hex strings).
type Palette struct {
	Primary   string `toml:"primary"`
	Secondary string `toml:"secondary"`
	Accent    string `toml:"accent"`
	Success   string `toml:"success"`
	Warning   string `toml:"warning"`
	Danger    string `toml:"danger"`
	Info      string `toml:"info"`
	Text      string `toml:"text"`
	Muted     string `toml:"muted"`
	Subtle    string `toml:"subtle"`
	Surface   string `toml:"surface"`
	Border    string `toml:"border"`
}

// Theme has a palette for dark and for light terminals.
type Theme struct {
	Dark  Palette `toml:"dark"`
	Light Palette `toml:"light"`
}

// Default returns the provisional palette.
func Default() Theme {
	return Theme{
		Dark: Palette{
			Primary: "#19C2B8", Secondary: "#8B6CFF", Accent: "#FF7A59",
			Success: "#3DD68C", Warning: "#F5B83D", Danger: "#FF5C7A", Info: "#5AB0FF",
			Text: "#ECEAF4", Muted: "#9591AD", Subtle: "#4A4562", Surface: "#221F30", Border: "#3A3650",
		},
		Light: Palette{
			Primary: "#00857D", Secondary: "#5B3FD6", Accent: "#D9512F",
			Success: "#16935A", Warning: "#B57900", Danger: "#C8223F", Info: "#1C6FD1",
			Text: "#1E1B2E", Muted: "#6B6880", Subtle: "#C9C5D8", Surface: "#F1EFF8", Border: "#D6D2E4",
		},
	}
}

// Load returns the default theme with the non-empty tokens of the TOML file
// at path applied. An empty path returns the default theme.
func Load(path string) (Theme, error) {
	t := Default()
	if path == "" {
		return t, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return t, fmt.Errorf("theme: %w", err)
	}
	var over Theme
	if err := toml.Unmarshal(b, &over); err != nil {
		return t, fmt.Errorf("theme %s: %w", path, err)
	}
	merge(&t.Dark, over.Dark)
	merge(&t.Light, over.Light)
	return t, nil
}

func merge(dst *Palette, src Palette) {
	dv, sv := reflect.ValueOf(dst).Elem(), reflect.ValueOf(src)
	for i := range sv.NumField() {
		if s := strings.TrimSpace(sv.Field(i).String()); s != "" {
			dv.Field(i).SetString(s)
		}
	}
}

// Styles are the ready-to-use styles for one background.
type Styles struct {
	IsDark bool

	Primary, Secondary, Accent           color.Color
	Success, Warning, Danger, Info       color.Color
	Text, Muted, Subtle, Surface, Border color.Color
	// Gradient is the brand gradient stops (logo, highlights, bars).
	Gradient []color.Color

	Base     lipgloss.Style
	Bold     lipgloss.Style
	Faint    lipgloss.Style
	Dim      lipgloss.Style
	Title    lipgloss.Style
	Heading  lipgloss.Style
	Key      lipgloss.Style
	Selected lipgloss.Style
	Card     lipgloss.Style
	Modal    lipgloss.Style
	TabOn    lipgloss.Style
	TabOff   lipgloss.Style
}

// Styles builds the styles for a dark or light background.
func (t Theme) Styles(isDark bool) *Styles {
	p := t.Light
	if isDark {
		p = t.Dark
	}
	c := lipgloss.Color
	s := &Styles{
		IsDark:  isDark,
		Primary: c(p.Primary), Secondary: c(p.Secondary), Accent: c(p.Accent),
		Success: c(p.Success), Warning: c(p.Warning), Danger: c(p.Danger), Info: c(p.Info),
		Text: c(p.Text), Muted: c(p.Muted), Subtle: c(p.Subtle), Surface: c(p.Surface), Border: c(p.Border),
	}
	s.Gradient = []color.Color{s.Primary, s.Secondary, s.Accent}
	s.Base = lipgloss.NewStyle().Foreground(s.Text)
	s.Bold = s.Base.Bold(true)
	s.Faint = lipgloss.NewStyle().Foreground(s.Muted)
	s.Dim = lipgloss.NewStyle().Foreground(s.Subtle)
	s.Title = lipgloss.NewStyle().Foreground(s.Primary).Bold(true)
	s.Heading = lipgloss.NewStyle().Foreground(s.Secondary).Bold(true)
	s.Key = lipgloss.NewStyle().Foreground(s.Primary).Bold(true)
	s.Selected = lipgloss.NewStyle().Background(s.Surface).Foreground(s.Text).Bold(true)
	s.Card = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(s.Border).Padding(0, 1)
	s.Modal = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(s.Secondary).Padding(1, 2)
	s.TabOn = lipgloss.NewStyle().Foreground(s.Surface).Background(s.Primary).Bold(true).Padding(0, 1)
	s.TabOff = lipgloss.NewStyle().Foreground(s.Muted).Padding(0, 1)
	return s
}

// Pill renders a rounded-looking badge with the given colour.
func (s *Styles) Pill(text string, bg color.Color) string {
	fg := lipgloss.Color("#FFFFFF")
	if s.IsDark {
		fg = lipgloss.Color("#14121C")
	}
	return lipgloss.NewStyle().Background(bg).Foreground(fg).Bold(true).Padding(0, 1).Render(text)
}

// Chip renders a subtle label: coloured dot plus text.
func (s *Styles) Chip(dot color.Color, text string) string {
	return lipgloss.NewStyle().Foreground(dot).Render("●") + " " + s.Faint.Render(text)
}

// GradientText colours each rune of a single-line string along the gradient.
func (s *Styles) GradientText(text string, bold bool) string {
	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}
	cols := lipgloss.Blend1D(max(len(runes), 2), s.Gradient...)
	var b strings.Builder
	for i, r := range runes {
		st := lipgloss.NewStyle().Foreground(cols[i]).Bold(bold)
		b.WriteString(st.Render(string(r)))
	}
	return b.String()
}

// GradientBlock colours a multi-line block column by column.
func (s *Styles) GradientBlock(block string) string {
	lines := strings.Split(block, "\n")
	w := 0
	for _, l := range lines {
		w = max(w, len([]rune(l)))
	}
	cols := lipgloss.Blend1D(max(w, 2), s.Gradient...)
	var b strings.Builder
	for li, l := range lines {
		for i, r := range []rune(l) {
			if r == ' ' {
				b.WriteRune(r)
				continue
			}
			b.WriteString(lipgloss.NewStyle().Foreground(cols[i]).Bold(true).Render(string(r)))
		}
		if li < len(lines)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}
