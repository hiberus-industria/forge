package tui

import (
	"context"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/hiberus-industria/forge/internal/daemon/localapi"
	"github.com/hiberus-industria/forge/internal/domain"
	"github.com/hiberus-industria/forge/internal/tui/theme"
)

// shared is the state every screen can read. Only the root model writes it.
type shared struct {
	ctx      context.Context
	be       Backend
	theme    theme.Theme
	st       *theme.Styles
	org      string
	currency domain.Currency
	// usdPerEUR is the latest ECB rate, derived from an EUR report; 0 = unknown.
	usdPerEUR float64
	status    *localapi.Status
	statusErr error
	width     int
	height    int
	now       func() time.Time
	exportDir string
	// titles caches issue titles ("owner/repo#n" → title) seen by any
	// screen, so the feed and the reports can name tasks.
	titles map[string]string
}

func (s *shared) title(ref string) string { return s.titles[ref] }

func (s *shared) money(micros int64) string { return money(micros, s.currency, s.usdPerEUR) }
func (s *shared) moneyAlt(micros int64) string {
	return moneyAlt(micros, s.currency, s.usdPerEUR)
}

// screen is one tab of the TUI.
type screen interface {
	Init() tea.Cmd
	// Update receives every non-key message, and key messages only when the
	// screen is active. Mouse coordinates are relative to the body.
	Update(msg tea.Msg) (screen, tea.Cmd)
	// SetSize gives the screen its body area. The root calls it whenever the
	// window or the surrounding chrome changes.
	SetSize(width, height int)
	// View renders the body at the last size set.
	View() string
	// Hints are the few bindings shown in the footer, most useful first.
	Hints() []key.Binding
	// Help lists every binding of the screen, grouped, for the ? overlay.
	Help() []helpGroup
	// Capturing is true while a text input has focus, so global shortcuts
	// (q, 1-4, ?) are passed through to the screen.
	Capturing() bool
}

// badged screens show a counter next to their tab.
type badged interface{ Badge() int }

type helpGroup struct {
	title string
	keys  []key.Binding
}

// ---------------------------------------------------------------- messages

type toastKind int

const (
	toastInfo toastKind = iota
	toastOK
	toastWarn
	toastErr
)

type toastMsg struct {
	text string
	kind toastKind
}

func toast(kind toastKind, text string) tea.Cmd {
	return func() tea.Msg { return toastMsg{text: text, kind: kind} }
}

type toastExpireMsg struct{ id int }

type statusMsg struct {
	st  localapi.Status
	err error
}

type statusTickMsg struct{}

type fxMsg struct{ usdPerEUR float64 }

type splashDoneMsg struct{}

// openModalMsg asks the root to show a modal on top of the current screen.
type openModalMsg struct{ m modal }

// closeModalMsg closes the current modal.
type closeModalMsg struct{}

func openModal(m modal) tea.Cmd { return func() tea.Msg { return openModalMsg{m} } }
func closeModal() tea.Msg       { return closeModalMsg{} }

// refreshMsg asks screens to reload their data (after lifecycle actions,
// reassignments, override changes...).
type refreshMsg struct{}

func refresh() tea.Msg { return refreshMsg{} }

// errMsg reports a failed background operation to the global banner.
type errMsg struct{ err error }
