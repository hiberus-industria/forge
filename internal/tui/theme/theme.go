// Package theme holds the TUI palette and the lipgloss styles built from it.
//
// The palette is deliberately sober: one accent (primary) for focus and
// selection, a neutral scale for text and chrome, and the semantic colours
// (success, warning, danger) reserved for status. Secondary, accent and info
// are kept for compatibility with existing theme files but the UI uses them
// sparingly. Every token can be overridden from a TOML file (config
// theme_file):
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
	// OnPrimary is the text drawn over a primary background (active tab).
	OnPrimary string `toml:"on_primary"`
}

// Theme has a palette for dark and for light terminals.
type Theme struct {
	Dark  Palette `toml:"dark"`
	Light Palette `toml:"light"`
}

// Default returns the default palette.
func Default() Theme {
	return Theme{
		Dark: Palette{
			Primary: "#2BB8AE", Secondary: "#9A93B8", Accent: "#D9926B",
			Success: "#62B98A", Warning: "#D6A548", Danger: "#E06C75", Info: "#7FA7D6",
			Text: "#E2E1E6", Muted: "#8D8B97", Subtle: "#55535E", Surface: "#2A2932", Border: "#3B3A44",
			OnPrimary: "#14131A",
		},
		Light: Palette{
			Primary: "#00796F", Secondary: "#5E587A", Accent: "#A85A33",
			Success: "#2E7D51", Warning: "#946200", Danger: "#B42335", Info: "#2F5F96",
			Text: "#24232B", Muted: "#6E6C78", Subtle: "#A9A7B2", Surface: "#E6E5EC", Border: "#D4D3DA",
			OnPrimary: "#FFFFFF",
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
	OnPrimary                            color.Color

	Base  lipgloss.Style // body text
	Bold  lipgloss.Style // emphasised body text (amounts, names)
	Faint lipgloss.Style // secondary text (labels, metadata)
	Dim   lipgloss.Style // tertiary text (separators, placeholders)
	// Hi is accent-coloured text, for the one thing that matters on screen.
	Hi lipgloss.Style
	// Section titles a block inside a panel.
	Section lipgloss.Style
	Key     lipgloss.Style // key hints
	// Cursor is the gutter mark of the focused row.
	Cursor lipgloss.Style
	// Sel is the background of the focused row.
	Sel lipgloss.Style
	// Link is a clickable (OSC 8) reference.
	Link lipgloss.Style
	// Rule is a horizontal or vertical separator.
	Rule  lipgloss.Style
	Modal lipgloss.Style
	// Tabs: the active one is a pill on the primary colour.
	TabOn, TabOff lipgloss.Style
	// Semantic text.
	OK, Warn, Err lipgloss.Style
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
		OnPrimary: c(p.OnPrimary),
	}
	s.Base = lipgloss.NewStyle().Foreground(s.Text)
	s.Bold = s.Base.Bold(true)
	s.Faint = lipgloss.NewStyle().Foreground(s.Muted)
	s.Dim = lipgloss.NewStyle().Foreground(s.Subtle)
	s.Hi = lipgloss.NewStyle().Foreground(s.Primary).Bold(true)
	s.Section = s.Faint.Bold(true)
	s.Key = s.Base.Bold(true)
	s.Cursor = lipgloss.NewStyle().Foreground(s.Primary)
	s.Sel = lipgloss.NewStyle().Background(s.Surface)
	s.Link = s.Base.Underline(true)
	s.Rule = lipgloss.NewStyle().Foreground(s.Border)
	s.Modal = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(s.Border).Padding(1, 2)
	s.TabOn = lipgloss.NewStyle().Foreground(s.OnPrimary).Background(s.Primary).Bold(true).Padding(0, 1)
	s.TabOff = lipgloss.NewStyle().Foreground(s.Muted).Padding(0, 1)
	s.OK = lipgloss.NewStyle().Foreground(s.Success)
	s.Warn = lipgloss.NewStyle().Foreground(s.Warning)
	s.Err = lipgloss.NewStyle().Foreground(s.Danger)
	return s
}

// Dot renders a status indicator: a coloured dot followed by muted text.
func (s *Styles) Dot(c color.Color, text string) string {
	return lipgloss.NewStyle().Foreground(c).Render("●") + " " + s.Faint.Render(text)
}
