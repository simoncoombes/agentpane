// Package ui renders the agentpane TUI (SPEC rev 3.1 PART 3, PART 4, §5.1,
// §5.2) on top of the frozen foundation packages. The render core is a pure
// function of (state.World, UIState, cols, rows, now, Palette): zero I/O,
// zero time.Now(), so snapshot tests can call it directly. The bubbletea
// Model in model.go wraps it.
package ui

import (
	"strconv"

	"github.com/simoncoombes/agentpane/internal/term"
)

// Style is one SGR rendition. agentpane emits raw ANSI 16-color SGR codes —
// never truecolor, never 256-color, never hex (§3.10.1) — so the emitted
// bytes are exactly what the PART 10 scans measure. fg is an ANSI index
// 0..15, or fgNone for the terminal's default foreground (§3.10.1 "primary
// text").
type Style struct {
	fg      int
	bold    bool
	faint   bool
	reverse bool

	// link, when set, is the URL the styled text is wrapped in as an OSC 8
	// hyperlink. It rides on the style rather than in the text because the
	// text is measured, cut and stripped of control characters long before
	// it is printed — see term.Link.
	link string
}

const fgNone = -1

// zero is the primary-text style: no SGR at all, so body text is whatever
// the user's scheme says it is (§3.10.1).
var zero = Style{fg: fgNone}

// Render wraps text in the style's SGR sequence. A zero style returns the
// text untouched. Empty text renders as empty.
func (s Style) Render(text string) string {
	if text == "" {
		return ""
	}
	out := text
	if seq := s.sgr(); seq != "" {
		out = seq + out + "\x1b[0m"
	}
	if s.link != "" {
		out = term.Link(s.link, out)
	}
	return out
}

// linkTo returns a copy of the style whose text is also an OSC 8 hyperlink to
// url. An empty url returns the style unchanged.
func (s Style) linkTo(url string) Style {
	s.link = url
	return s
}

func (s Style) sgr() string {
	var codes []string
	if s.bold {
		codes = append(codes, "1")
	}
	if s.faint {
		codes = append(codes, "2")
	}
	if s.reverse {
		codes = append(codes, "7")
	}
	if s.fg >= 0 && s.fg <= 7 {
		codes = append(codes, strconv.Itoa(30+s.fg))
	} else if s.fg >= 8 && s.fg <= 15 {
		codes = append(codes, strconv.Itoa(90+(s.fg-8)))
	}
	if len(codes) == 0 {
		return ""
	}
	out := "\x1b["
	for i, c := range codes {
		if i > 0 {
			out += ";"
		}
		out += c
	}
	return out + "m"
}

func (s Style) isZero() bool {
	return !s.bold && !s.faint && !s.reverse && (s.fg < 0 || s.fg > 15)
}

// withReverse returns the style with reverse video added (selection, §3.10.2).
func (s Style) withReverse() Style { s.reverse = true; return s }

// withFaint returns the style with the dim attribute added (§3.9 disconnected
// tree, §3.10.1 deepest chrome).
func (s Style) withFaint() Style { s.faint = true; return s }

// Palette implements §3.10.1 / §3.10.2a exactly: three semantic states (live,
// needs-you, settled) plus primary text, on one hue. Needs-you carries NO hue
// of its own — it is bold (bright) + the ⚑ glyph + reverse video on the band
// chip. Error color (ANSI 1/9) exists only for inspector log text.
type Palette struct {
	NoColor bool

	// Primary is the default foreground — no SGR (§3.10.1 row 1).
	Primary Style
	// Live is the auto-picked accent (§3.10.3), or dim weight when no
	// distinguishable index exists (term.AccentWeightOnly, §3.10.2a).
	Live Style
	// NeedsYou is bright weight only: bold, no hue (§3.10.2a).
	NeedsYou Style
	// Chip is the reverse-video band label (§3.10.2): NeedsYou + reverse.
	Chip Style
	// Settled is ANSI 8 grey (done, queued, decayed, structure).
	Settled Style
	// Struct draws the trunk, branches, and separators — ANSI 8, never 0.
	Struct Style
	// Deep is the deepest chrome: footer hints, station labels (8 + dim).
	Deep Style
	// Err / ErrBright are ANSI 1 / 9 — inspector log text ONLY (§3.10.2a).
	Err       Style
	ErrBright Style
	// Ok marks ✔ tool calls and the inferred ✓ verified (ANSI 2).
	Ok Style
	// Meta names what an agent is RUNNING ON — its model and effort, on the row
	// and in the header.
	//
	// It is the one deliberate break from §3.10.1's single hue. That rule keeps
	// the pane's STATES on one scale, so nothing competes with needs-you: live,
	// settled and deep are degrees of the same thing. A model id is not a state
	// at all — it never changes, it says nothing about how the agent is doing —
	// and rendered in a degree of the state hue it read as a dim state and was
	// unreadable besides. A second hue says "different kind of fact" in the one
	// place the pane has a different kind of fact.
	//
	// Bright blue, except where the accent is already blue: two hues are only
	// worth having if they are two.
	Meta Style
}

// NewPalette builds the palette for the resolved accent index (§3.10.3;
// term.AccentWeightOnly for the weight-only fallback) and the NO_COLOR mode
// (§3.10.4: no SGR color at all — glyphs, dim, reverse, and bold carry the
// difference).
func NewPalette(accent int, noColor bool) Palette {
	if noColor {
		return Palette{
			NoColor:   true,
			Primary:   zero,
			Live:      zero,
			NeedsYou:  Style{fg: fgNone, bold: true},
			Chip:      Style{fg: fgNone, bold: true, reverse: true},
			Settled:   Style{fg: fgNone, faint: true},
			Struct:    Style{fg: fgNone, faint: true},
			Deep:      Style{fg: fgNone, faint: true},
			Err:       zero,
			ErrBright: Style{fg: fgNone, bold: true},
			Ok:        zero,
			Meta:      Style{fg: fgNone, faint: true},
		}
	}
	live := Style{fg: accent}
	if accent < 0 || accent > 15 {
		// term.AccentWeightOnly: live renders dim against normal-weight
		// primary (§3.10.2a).
		live = Style{fg: fgNone, faint: true}
	}
	meta := Style{fg: 12} // bright blue
	if accent == 4 || accent == 12 {
		meta = Style{fg: 14} // …unless the accent is blue, then bright cyan
	}
	return Palette{
		Primary:   zero,
		Live:      live,
		NeedsYou:  Style{fg: fgNone, bold: true},
		Chip:      Style{fg: fgNone, bold: true, reverse: true},
		Settled:   Style{fg: 8},
		Struct:    Style{fg: 8},
		Deep:      Style{fg: 8, faint: true},
		Err:       Style{fg: 1},
		ErrBright: Style{fg: 9},
		Ok:        Style{fg: 2},
		Meta:      meta,
	}
}
