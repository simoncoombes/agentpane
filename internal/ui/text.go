package ui

import (
	"strings"

	"github.com/mattn/go-runewidth"
)

// seg is one styled run of text. Lines are []seg so selection can reverse a
// whole row and the disconnected state can dim a whole region without string
// surgery on escape sequences.
type seg struct {
	text string
	st   Style
}

// t is the seg's renderable text. Everything that measures, cuts or prints a
// seg goes through here, so the width the layout budgeted is exactly the width
// the terminal receives — see flatten for why raw text cannot be trusted.
func (s seg) t() string { return flatten(s.text) }

func segsWidth(segs []seg) int {
	w := 0
	for _, s := range segs {
		w += runewidth.StringWidth(s.t())
	}
	return w
}

func renderSegs(segs []seg) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.st.Render(s.t()))
	}
	return b.String()
}

// flatten is the last line of defence for the frame's geometry: it strips the
// control characters that carry no display width but move the cursor anyway.
//
// Every string on a row is platform text - a Bash command, a file path, an
// assistant message - and any of them can contain a newline. A `python3 -c`
// one-liner routinely does. runewidth counts \n as zero columns, so neither
// the width budget nor truncSegs sees it, and the row silently becomes several
// physical lines: the tree overflows the pane and the terminal scrolls. Callers
// that want a MEANINGFUL single line use oneLine, which keeps the first line
// and drops the rest; this only guarantees that whatever they produced cannot
// break the frame. Tabs become spaces (a tab jumps to the next tab stop, which
// is not the width that was measured); ESC is dropped so text can never inject
// its own styling.
func flatten(s string) string {
	// The fast path must test the whole control range, not a hand-picked list:
	// a BEL smuggled through in a command would ring the terminal on every
	// repaint, and DEL and the C1 escapes move the cursor just as well as \n.
	clean := true
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			clean = false
			break
		}
	}
	if clean {
		return s // the common path allocates nothing
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case r == '\n' || r == '\r':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, s)
}

// truncSegs cuts a styled line to max columns, ending with … when anything
// was dropped. Styles of surviving runs are preserved.
func truncSegs(segs []seg, max int) []seg {
	if max <= 0 {
		return nil
	}
	if segsWidth(segs) <= max {
		return segs
	}
	budget := max - 1 // room for the ellipsis
	var out []seg
	for _, s := range segs {
		w := runewidth.StringWidth(s.t())
		if w <= budget {
			out = append(out, seg{s.t(), s.st})
			budget -= w
			continue
		}
		if budget > 0 {
			out = append(out, seg{truncString(s.t(), budget), s.st})
		}
		out = append(out, seg{"…", s.st})
		return out
	}
	out = append(out, seg{"…", zero})
	return out
}

// truncString cuts a plain string to max display columns (no ellipsis).
func truncString(s string, max int) string {
	if max <= 0 {
		return ""
	}
	w := 0
	for i, r := range s {
		rw := runewidth.RuneWidth(r)
		if w+rw > max {
			return s[:i]
		}
		w += rw
	}
	return s
}

// composeLR lays out a left block and a right-aligned block on one line of
// exactly width columns (right-aligned per the §3.1 row anatomy). The left
// block is truncated first if the two collide — callers must have already
// applied the §3.1 narrow degradation so the slug is never the victim. When
// right is empty the line is left-aligned and padded to width.
func composeLR(left, right []seg, width int) []seg {
	rw := segsWidth(right)
	avail := width - rw
	if rw > 0 {
		avail-- // minimum one space gap
	}
	if avail < 0 {
		avail = 0
	}
	left = truncSegs(left, avail)
	gap := width - segsWidth(left) - rw
	out := append([]seg(nil), left...)
	if gap > 0 {
		out = append(out, seg{strings.Repeat(" ", gap), zero})
	}
	out = append(out, right...)
	return out
}

// reverseLine applies reverse video to a whole line (§3.10.2 selected row).
//
// The reversal is UNIFORM: every run is re-styled to the same reversed
// primary style, discarding the per-run colours. Reversing each run in place
// instead would swap each one's own foreground into its background, so a row
// mixing primary (the user's default fg) with settled (ANSI 8) paints as
// alternating blocks of two different colours — in a green-on-black scheme,
// green boxes with grey gaps rather than one selection bar. A selected row is
// one object; it gets one bar. Bold is preserved because it carries meaning
// (needs-you, §3.10.2a) that colour alone no longer does.
func reverseLine(segs []seg) []seg {
	out := make([]seg, len(segs))
	for i, s := range segs {
		st := zero.withReverse()
		st.bold = s.st.bold
		s.st = st
		out[i] = s
	}
	return out
}

// penGlyphs are the frontier of a line being drawn: a thin left edge that
// thickens and thins as it travels, so the tip shimmers rather than sliding
// along as a dead mark. All four are single-width block elements, so the cell
// the pen occupies is the same cell whichever one it is.
var penGlyphs = [...]string{"▏", "▎", "▍", "▎"}

// penGlyph is the tip at a given point in the draw: the phase is the column,
// so the shimmer is a property of the stroke and not of the wall clock — the
// same frame always draws the same thing.
func penGlyph(col int) string {
	if col < 0 {
		col = -col
	}
	return penGlyphs[(col/2)%len(penGlyphs)]
}

// drawSegs reveals the first `cols` display columns of a composed line, marks
// the frontier with the pen, and blanks the rest — so the line is drawn across
// the row at a CONSTANT width.
//
// Constant width is the whole point: an arriving row must not move anything
// already on screen, and every line in this pane has a right-aligned column
// that would jump if the reveal shortened it. Blanking rather than truncating
// also means the draw costs no layout arithmetic — the line was already
// composed to exactly the width it will end at.
// trailCells is how much of the ink immediately behind the pen is still wet:
// drawn, but rendered in the pen's own colour and settling to its real style as
// the pen moves on. It is what makes the reveal read as a stroke rather than as
// a boundary moving across a finished line.
const trailCells = 10

func drawSegs(segs []seg, cols int, edge Style) []seg {
	total := segsWidth(segs)
	if cols >= total {
		return segs
	}
	if cols < 0 {
		cols = 0
	}
	wet := cols - trailCells // where the ink is still the pen's colour
	out := make([]seg, 0, len(segs)+4)
	col := 0
	penned := false
	blank := func(w int) {
		if w <= 0 {
			return
		}
		if !penned {
			penned = true
			tip := edge
			tip.bold = true
			out = append(out, seg{penGlyph(cols), tip})
			w--
		}
		if w > 0 {
			out = append(out, seg{strings.Repeat(" ", w), zero})
		}
	}
	// put emits text at column `col`, splitting it across the dry/wet boundary
	// so a run that straddles it is half settled and half still the pen's.
	put := func(text string, st Style) {
		for text != "" {
			w := runewidth.StringWidth(text)
			switch {
			case col+w <= wet: // all dry
				out = append(out, seg{text, st})
				col += w
				return
			case col >= wet: // all wet
				out = append(out, seg{text, edge})
				col += w
				return
			default: // straddles: emit the dry head, loop for the rest
				head := truncString(text, wet-col)
				if head == "" {
					head = string([]rune(text)[:1])
				}
				out = append(out, seg{head, st})
				col += runewidth.StringWidth(head)
				text = text[len(head):]
			}
		}
	}
	left := cols
	for _, s := range segs {
		w := runewidth.StringWidth(s.t())
		switch {
		case left >= w:
			put(s.t(), s.st)
			left -= w
		case left > 0:
			head := truncString(s.t(), left)
			put(head, s.st)
			blank(w - runewidth.StringWidth(head))
			left = 0
		default:
			blank(w)
		}
	}
	return out
}

// litFirstRune re-styles a line's first character — the trunk cell — leaving
// everything else alone. It is how the pulse travels: one cell at a time down a
// column that is already drawn, so nothing moves and nothing is added.
func litFirstRune(segs []seg, st Style) []seg {
	for i, s := range segs {
		t := s.t()
		if t == "" {
			continue
		}
		r := []rune(t)
		out := make([]seg, 0, len(segs)+1)
		out = append(out, segs[:i]...)
		out = append(out, seg{string(r[0]), st})
		if len(r) > 1 {
			out = append(out, seg{string(r[1:]), s.st})
		}
		out = append(out, segs[i+1:]...)
		return out
	}
	return segs
}

// pad fits s to exactly w display columns: cut with an ellipsis when it is too
// long, padded with spaces when it is short. It is what makes a list of rows a
// table — the eye can only run down a column that is actually a column.
func pad(s string, w int) string {
	if w <= 0 {
		return ""
	}
	n := runewidth.StringWidth(s)
	if n == w {
		return s
	}
	if n > w {
		return truncString(s, w-1) + "…"
	}
	return s + strings.Repeat(" ", w-n)
}

// faintLine dims every run of a line (§3.9 disconnected: tree dimmed 50%).
func faintLine(segs []seg) []seg {
	out := make([]seg, len(segs))
	for i, s := range segs {
		s.st = s.st.withFaint()
		out[i] = s
	}
	return out
}

// line builds a []seg from alternating (text, style) pairs, skipping empties.
func line(parts ...seg) []seg {
	var out []seg
	for _, p := range parts {
		if p.text != "" {
			out = append(out, p)
		}
	}
	return out
}
