// Package term owns every raw terminal interaction agentpane performs:
// the one-shot capability probe (OSC 4 / OSC 10 palette queries, §3.10.3),
// the accent auto-pick, and the escape emitters for titles, badges,
// notifications, hyperlinks, clipboard, bell, and focus reporting
// (§3.19, §5.3, §5.4).
//
// Everything here degrades silently: a disabled emitter writes nothing, a
// failed write is swallowed, an unanswered probe returns zero capabilities.
// Nothing in this package may break a render (§5.4).
package term

import (
	"encoding/base64"
	"io"
	"os/exec"
	"strings"
)

// Emitter writes terminal escape sequences to W. The zero value is disabled:
// every method is a silent no-op, so a caller that never probed a tty can
// hold one safely. Write errors are ignored by design — a missing capability
// must never break a render (§5.4).
type Emitter struct {
	W       io.Writer
	Enabled bool

	// CopyFallback receives the yanked text after the OSC 52 write, because
	// OSC 52 is silently ignored when iTerm2's clipboard-access preference is
	// off. Nil means "pipe to pbcopy if it exists". Set it in tests.
	CopyFallback func(text string) error
}

func (e Emitter) write(s string) {
	if !e.Enabled || e.W == nil {
		return
	}
	io.WriteString(e.W, s) //nolint:errcheck // degrade silently (§5.4)
}

// sanitize strips C0 control bytes and DEL from user-influenced text so an
// agent name can never smuggle its own escape sequence into an OSC payload.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// SetTitle sets the pane title via OSC 1 (§5.4: "⚑ <slug> needs you").
func (e Emitter) SetTitle(title string) {
	e.write("\x1b]1;" + sanitize(title) + "\a")
}

// ClearTitle restores an empty pane title.
func (e Emitter) ClearTitle() {
	e.write("\x1b]1;\a")
}

// Badge sets the iTerm2 badge via OSC 1337 SetBadgeFormat (base64 payload).
func (e Emitter) Badge(format string) {
	b := base64.StdEncoding.EncodeToString([]byte(sanitize(format)))
	e.write("\x1b]1337;SetBadgeFormat=" + b + "\a")
}

// ClearBadge removes the iTerm2 badge.
func (e Emitter) ClearBadge() {
	e.write("\x1b]1337;SetBadgeFormat=\a")
}

// RequestAttention asks for one dock bounce (OSC 1337, §5.3: on a new ask).
func (e Emitter) RequestAttention() {
	e.write("\x1b]1337;RequestAttention=1\a")
}

// Notify posts one system notification via OSC 9 (escalation level 4, §3.20).
func (e Emitter) Notify(text string) {
	e.write("\x1b]9;" + sanitize(text) + "\a")
}

// Bell rings the terminal bell once. Callers own the once-per-event rule
// (§3.7 item 4).
func (e Emitter) Bell() {
	e.write("\a")
}

// Hyperlink returns text wrapped in an OSC 8 hyperlink for embedding in a
// rendered row (§5.4: every path and file:line). Unlike the other emitters it
// returns the string rather than writing it, because links are laid out by
// the renderer. Disabled or empty-URL calls return text unchanged.
func (e Emitter) Hyperlink(url, text string) string {
	if !e.Enabled || url == "" {
		return text
	}
	return "\x1b]8;;" + sanitize(url) + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// CopyToClipboard yanks text via OSC 52 (works over SSH/tmux) and then runs
// the pbcopy fallback, because OSC 52 gives no success signal and is commonly
// disabled in iTerm2 preferences (§5.3). Both paths carry identical content,
// so double delivery is harmless. Disabled emitters do nothing at all.
func (e Emitter) CopyToClipboard(text string) {
	if !e.Enabled {
		return
	}
	b := base64.StdEncoding.EncodeToString([]byte(text))
	e.write("\x1b]52;c;" + b + "\a")
	fb := e.CopyFallback
	if fb == nil {
		fb = pbcopy
	}
	fb(text) //nolint:errcheck // degrade silently (§5.4)
}

func pbcopy(text string) error {
	path, err := exec.LookPath("pbcopy")
	if err != nil {
		return err
	}
	cmd := exec.Command(path)
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

// EnableFocusReporting emits CSI ?1004h. Bubbletea delivers FocusMsg/BlurMsg
// itself when the program runs (tea.WithReportFocus); this emitter and its
// pair exist only so `agentpane doctor` can probe the capability outside a
// bubbletea program (§3.19).
func (e Emitter) EnableFocusReporting() {
	e.write("\x1b[?1004h")
}

// DisableFocusReporting emits CSI ?1004l. See EnableFocusReporting.
func (e Emitter) DisableFocusReporting() {
	e.write("\x1b[?1004l")
}
