package ui

import (
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/simoncoombes/agentpane/internal/narrate"
	"github.com/simoncoombes/agentpane/internal/state"
)

// The narration region (§13.2, §13.4).
//
// Two facts about this file matter more than anything in it.
//
// **It is fixed-height.** renderNarration returns exactly narrationHeight(mode)
// lines for every world, every scene, every voice mode and every sentence
// length, padding when there is less to say and scrolling internally when there
// is more. Nothing here measures its content and then asks the frame for room:
// that is the specific bug §13.4 forbids, because a pane that reflows while you
// read it is one you stop trusting.
//
// **It sits BELOW the tree.** That is what makes the §13.4 invariant true by
// construction rather than by arithmetic: the tree's first row is fixed by the
// header and the alert band, so turning the voice on, cycling it, or writing a
// 200-character sentence cannot move a single tree row. Putting narration above
// the tree would move every row every time the mode changed, and no amount of
// careful height accounting would fix it.

// VoiceMode is the §13.2 cycle: standup + commentary → standup → off.
type VoiceMode string

const (
	VoiceFull    VoiceMode = "full" // standup + commentary
	VoiceStandup VoiceMode = "standup"
	VoiceOff     VoiceMode = "off"
)

// next is the `n` cycle. off wraps back to full, so the key is a cycle and not
// a one-way switch.
func (m VoiceMode) next() VoiceMode {
	switch m {
	case VoiceFull:
		return VoiceStandup
	case VoiceStandup:
		return VoiceOff
	default:
		return VoiceFull
	}
}

// degraded is the geometry ladder, which is NOT the cycle: it only ever loses
// regions, and it stops at off. See narrationPlan.
func (m VoiceMode) degraded() VoiceMode {
	switch m {
	case VoiceFull:
		return VoiceStandup
	default:
		return VoiceOff
	}
}

func (m VoiceMode) label() string {
	switch m {
	case VoiceFull:
		return "standup + commentary"
	case VoiceStandup:
		return "standup"
	default:
		return "off"
	}
}

// Region heights, in rows, from the §13.2 table. They are constants and there is
// no code path that computes them from content.
//
// The standup grows to 11 rows when the commentary is hidden because the
// commentary is where the long form lives; with it gone, the standup is the only
// prose on screen and gets the space. That is also the affordance behind the
// overflow hint: `n` genuinely reveals more standup.
const (
	standupRowsWithLog = 7
	standupRowsAlone   = 11
	commentaryRows     = 11

	// standupChromeRows are the sticky chip and the sticky action — the two rows
	// of a standup that are never scrolled away. The chip because it is the
	// verdict; the action because it is the point.
	standupChromeRows = 2
	// commentaryChromeRows is the COMMENTARY header.
	commentaryChromeRows = 1
)

// standupRows is the standup region's row count for a mode.
func standupRows(mode VoiceMode) int {
	if mode == VoiceFull {
		return standupRowsWithLog
	}
	return standupRowsAlone
}

// narrationHeight is the total rows the narration owns, INCLUDING the separator
// rule above each region. It is a pure function of the mode: that is the whole
// §13.4 guarantee, and every other height in this file is derived from it.
func narrationHeight(mode VoiceMode) int {
	switch mode {
	case VoiceFull:
		return 1 + standupRowsWithLog + 1 + commentaryRows
	case VoiceStandup:
		return 1 + standupRowsAlone
	default:
		return 0
	}
}

// standupBodyRows / commentaryBodyRows are the scrollable middles.
func standupBodyRows(mode VoiceMode) int {
	n := standupRows(mode) - standupChromeRows
	if n < 1 {
		n = 1
	}
	return n
}

func commentaryBodyRows() int { return commentaryRows - commentaryChromeRows }

// commentaryPage is how far ⇞/⇟ move: a page less one line, so the line you were
// reading is still on screen after the jump.
func commentaryPage() int {
	if n := commentaryBodyRows() - 1; n > 0 {
		return n
	}
	return 1
}

// narrateNow composes the standup for the current frame.
//
// The commentary comes from UIState.Narr, which the Model advances as events
// arrive; the standup is re-derived here because it is a pure function of the
// world and must always describe the frame being drawn, not the frame that
// happened to produce the last event. A nil-ish (zero) Memory still yields a
// full standup — that is what lets the snapshot tests call renderFrame directly.
func narrateNow(w state.World, v *UIState, now time.Time) narrate.Standup {
	return narrate.Compose(w, v.Narr, now, narrate.Options{Name: v.slugFor})
}

// advanceNarration folds a world into the narrator's memory and returns the
// entries that were appended.
//
// It lives on UIState rather than on the Model so the render path stays pure:
// renderFrame only ever READS Narr, and everything that grows it happens here,
// once per machine update. Tests (and the goldens) call it directly for the same
// reason the Model does — it is the whole of the commentary's lifecycle.
func (v *UIState) advanceNarration(w state.World, now time.Time, width int) []narrate.Entry {
	r := narrate.Narrate(w, v.Narr, now, narrate.Options{Name: v.slugFor})
	v.Narr = r.Memory

	// A slug that has appeared in the standup or the commentary is frozen
	// (§13.1(a)): the spec is explicit that a name changing under the reader is
	// worse than a slightly wrong one, and prose is exactly where a reader would
	// have read it. The late exact description is still recorded and shows in the
	// inspector; the row keeps the name that was published.
	//
	// "Appeared" means DRAWN. Publishing here, on every machine update, froze
	// names nobody could have read: with the voice off, at a geometry where the
	// region is withheld, or for entries clipped out of the commentary window.
	// That silently defeated the guess→exact correction §13.1 explicitly keeps.
	// The pending set is published by the renderer instead, once a frame that
	// actually contains those names has been produced.
	v.pendingNamed = append(v.pendingNamed, r.Named...)

	// Hold a paused view still. CommScroll counts rendered lines up from the
	// bottom, so appending entries would otherwise slide the window out from
	// under the reader; adding the new lines' height keeps the same text on
	// screen. At offset zero — auto-follow — nothing is adjusted, which is what
	// makes the newest line arrive at the bottom.
	if len(r.New) > 0 && v.CommScroll > 0 {
		v.CommScroll += commentaryTotalLines(r.New, width)
	}
	return r.New
}

// toneStyle maps a narrate.Tone onto the pane's existing semantic styles.
//
// Deliberately lossy: §3.10.2a allows three semantic states and narration does
// not get to invent a fourth. ToneWatch and ToneBody both land on primary text,
// which means a "worth watching" sentence is distinguished by its WORDS and its
// position in the block, not by a colour the rest of the pane does not have.
func toneStyle(t narrate.Tone, pal Palette) Style {
	switch t {
	case narrate.ToneAlert:
		return pal.NeedsYou
	case narrate.ToneWatch, narrate.ToneBody:
		return pal.Primary
	case narrate.ToneDim:
		return pal.Settled
	default:
		return pal.Deep
	}
}

// chipStyle styles the sticky chip the way the alert band styles its own
// (§3.10.2): reverse video when you are the blocker, bright when something wants
// watching, grey otherwise.
func chipStyle(st narrate.Standup, pal Palette) Style {
	switch st.Scene {
	case narrate.SceneAlert:
		return pal.Chip
	case narrate.SceneWatch:
		return pal.NeedsYou
	default:
		return pal.Settled
	}
}

// renderNarration renders the whole narration region: exactly
// narrationHeight(mode) lines, rules included.
func renderNarration(w state.World, v *UIState, mode VoiceMode, width int, now time.Time, pal Palette) [][]seg {
	if mode == VoiceOff {
		return nil
	}
	st := narrateNow(w, v, now)
	out := renderStandup(st, v, mode, width, pal)
	if mode == VoiceFull {
		out = append(out, renderCommentary(v, width, pal)...)
	}
	// This frame is the one the reader sees, so this is where a name becomes
	// "published" and its slug freezes (§13.1(a)). Names the narrator produced
	// for a region that was never drawn stay correctable.
	v.publishDrawnNames(v.pendingNamed)
	// Height is asserted here rather than trusted: a padding bug in either
	// region would otherwise move the footer, which is the failure §13.4 exists
	// to prevent. Clipping and padding to the declared height makes the
	// guarantee unconditional.
	want := narrationHeight(mode)
	for len(out) < want {
		out = append(out, nil)
	}
	return out[:want]
}

// renderStandup draws the standup region: rule, sticky chip, scrolling body,
// sticky action.
func renderStandup(st narrate.Standup, v *UIState, mode VoiceMode, width int, pal Palette) [][]seg {
	out := [][]seg{rule(width, pal)}

	out = append(out, composeLR(
		line(seg{st.Chip, chipStyle(st, pal)}),
		line(seg{st.Clock, pal.Deep}), width))

	body := standupBody(st, width, pal)
	avail := standupBodyRows(mode)
	if len(body) <= avail {
		for _, b := range body {
			out = append(out, b.segs)
		}
		for i := len(body); i < avail; i++ {
			out = append(out, nil)
		}
	} else {
		// Clipped. The last row is spent on stating the clip rather than on one
		// more half-sentence, because the reader's question is "is there
		// something under here I need" and the answer to that is worth more than
		// four extra words.
		show := avail - 1
		off := v.StandupScroll
		if off > len(body)-show {
			off = len(body) - show
		}
		if off < 0 {
			off = 0
		}
		for _, b := range body[off : off+show] {
			out = append(out, b.segs)
		}
		out = append(out, truncSegs(line(seg{standupClipHint(mode, body, off, show), pal.Deep}), width))
	}

	out = append(out, truncSegs(line(seg{st.Action.Text, toneStyle(st.Action.Tone, pal)}), width))
	return out
}

// standupClipHint names what was cut and how to see it.
//
// It names the PARTS that were cut, not just the line count: §13.2's order means
// the clip always falls on the least urgent end of the block, so the only thing
// the reader needs to decide is whether the hidden end matters to them. "+12
// more" cannot answer that; "+12 more (risk · state)" can.
//
// With the commentary hidden the page keys own this body and the hint says so.
// With the commentary shown they belong to the log, and the honest advice is that
// `n` trades the log for four more standup rows.
func standupClipHint(mode VoiceMode, body []bodyLine, off, show int) string {
	hidden := map[narrate.Part]bool{}
	var order []narrate.Part
	for i, b := range body {
		if i >= off && i < off+show {
			continue
		}
		if !hidden[b.part] {
			hidden[b.part] = true
			order = append(order, b.part)
		}
	}
	names := make([]string, 0, len(order))
	for _, p := range order {
		names = append(names, p.String())
	}
	cut := strings.Join(names, ", ")
	rest := len(body) - off - show
	if mode == VoiceStandup {
		return "↕ " + itoa(off+1) + "–" + itoa(off+show) + " of " + itoa(len(body)) +
			" · ⇞⇟ · off screen: " + cut
	}
	return "+" + itoa(rest) + " more (" + cut + ") · n for room"
}

// bodyLine is one rendered standup line and the part it came from, so the clip
// hint can say what it cut.
type bodyLine struct {
	segs []seg
	part narrate.Part
}

// standupBody wraps the standup's sentences into rendered lines. Parts are not
// separated by blank rows: §3.1 asks for tight leading, and a blank row in a
// seven-row region costs a sentence.
func standupBody(st narrate.Standup, width int, pal Palette) []bodyLine {
	var out []bodyLine
	for _, l := range st.Lines {
		style := toneStyle(l.Tone, pal)
		for _, wrapped := range wrapAll(l.Text, width) {
			out = append(out, bodyLine{truncSegs(line(seg{wrapped, style}), width), l.Part})
		}
	}
	return out
}

// renderCommentary draws the commentary region: rule, header, then the newest
// lines at the bottom.
func renderCommentary(v *UIState, width int, pal Palette) [][]seg {
	out := [][]seg{rule(width, pal)}

	body, heads := commentaryLines(v.Narr.Entries, width, pal)
	avail := commentaryBodyRows()
	scroll := clampCommScroll(v.CommScroll, len(body), avail)

	right := itoa(len(v.Narr.Entries)) + " entries"
	switch {
	case len(body) <= avail:
		// Nothing to scroll: saying "⇞⇟ scroll" here would advertise a key that
		// does nothing.
	case scroll > 0:
		right += " · ⇞⇟ paused"
	default:
		right += " · ⇞⇟ scroll"
	}
	out = append(out, composeLR(
		line(seg{"COMMENTARY", pal.Deep}),
		line(seg{right, pal.Deep}), width))

	if len(body) == 0 {
		// Honest absence: an empty log is not a broken one, and the reason it is
		// empty is worth a row.
		out = append(out, truncSegs(line(seg{copyNoCommentary, pal.Deep}), width))
		for i := 1; i < avail; i++ {
			out = append(out, nil)
		}
		return out
	}

	end := len(body) - scroll
	start := end - avail
	if start < 0 {
		start = 0
	}
	// Start on an entry, never mid-sentence. A window that opens on a wrapped
	// continuation puts a fragment like "      it." at the top of the region,
	// which reads as a rendering fault rather than as scrollback. `end` is left
	// alone — the newest line stays on the last row, which is the whole point of
	// the bottom anchor — and the rows the alignment gives up become blanks
	// ABOVE the first entry.
	for start < end && !heads[start] {
		start++
	}
	window := body[start:end]
	pad := avail - len(window)
	if len(body) <= avail {
		// A log that fits grows DOWN from the top, the way a terminal does: its
		// first line is its first line, not something floating mid-region.
		out = append(out, window...)
		for i := 0; i < pad; i++ {
			out = append(out, nil)
		}
		return out
	}
	// A full window is bottom-anchored, so any slack the entry alignment gave up
	// sits above the oldest visible entry rather than under the newest.
	for i := 0; i < pad; i++ {
		out = append(out, nil)
	}
	out = append(out, window...)
	return out
}

const copyNoCommentary = "nothing worth a sentence yet"

// clampCommScroll keeps the offset inside the log. Auto-follow is not a separate
// flag — it IS offset zero, which is the only way the two cannot drift apart:
// §13.2 wants follow to pause when the user scrolls up and resume when they
// return to the bottom, and "the bottom" is exactly offset zero.
func clampCommScroll(scroll, total, avail int) int {
	if scroll < 0 {
		return 0
	}
	if max := total - avail; scroll > max {
		if max < 0 {
			max = 0
		}
		return max
	}
	return scroll
}

// commentaryLines renders the entries into styled lines, oldest first, and
// reports which of those lines START an entry.
//
// An entry wraps across as many lines as it needs; the timestamp is printed once,
// on its first line, and continuations are indented under the text so the clock
// stays readable as a column. The heads slice is what lets the window open on a
// whole entry instead of on a fragment.
func commentaryLines(entries []narrate.Entry, width int, pal Palette) (lines [][]seg, heads []bool) {
	stamp := commentaryStampWidth
	indent := strings.Repeat(" ", stamp)
	textWidth := width - stamp
	if textWidth < 8 {
		textWidth = 8
	}
	for _, e := range entries {
		style := toneStyle(e.Tone, pal)
		for i, ln := range wrapAll(e.Text, textWidth) {
			if i == 0 {
				lines = append(lines, truncSegs(line(
					seg{padRight(commentaryStamp(e.At), stamp), pal.Deep},
					seg{ln, style}), width))
				heads = append(heads, true)
				continue
			}
			lines = append(lines, truncSegs(line(seg{indent, zero}, seg{ln, style}), width))
			heads = append(heads, false)
		}
	}
	return lines, heads
}

// commentaryStampWidth is "m:ss" plus a space, widened to fit an hour-long run
// ("1:04:12" would not fit in five, and a run that long is not unusual).
const commentaryStampWidth = 6

func commentaryStamp(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	sec := int(d / time.Second)
	if sec < 3600 {
		return formatClock(d)
	}
	return itoa(sec/3600) + ":" + pad2(sec%3600/60) + ":" + pad2(sec%60)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

func padRight(s string, w int) string {
	if n := w - runewidth.StringWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// wrapAll wraps on spaces to at most w columns, with no line cap: the region's
// own height does the clipping, so a long sentence is scrollable rather than
// silently amputated (which is what inspector.go's wrapText does on purpose, for
// a two-line budget it cannot exceed).
func wrapAll(s string, w int) []string {
	if w < 8 {
		w = 8
	}
	s = flatten(s)
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	cur := ""
	flush := func() {
		if cur != "" {
			out = append(out, cur)
			cur = ""
		}
	}
	for _, word := range strings.Fields(s) {
		for runewidth.StringWidth(word) > w {
			// A single unbreakable token longer than the pane (a path, a URL, a
			// quoted command): cut it at the width rather than overflow the
			// frame. The break is visible because the next line continues it.
			flush()
			head := truncString(word, w)
			out = append(out, head)
			word = word[len(head):]
		}
		switch {
		case cur == "":
			cur = word
		case runewidth.StringWidth(cur)+1+runewidth.StringWidth(word) <= w:
			cur += " " + word
		default:
			flush()
			cur = word
		}
	}
	flush()
	return out
}

// commentaryTotalLines measures the rendered height of the log at a width. The
// Model needs it to hold a paused view still as new entries arrive, and the page
// keys need it to clamp.
func commentaryTotalLines(entries []narrate.Entry, width int) int {
	stamp := commentaryStampWidth
	textWidth := width - stamp
	if textWidth < 8 {
		textWidth = 8
	}
	n := 0
	for _, e := range entries {
		if k := len(wrapAll(e.Text, textWidth)); k > 0 {
			n += k
		}
	}
	return n
}

// publishDrawnNames freezes the slugs of agents whose names reached a frame the
// reader could actually see. renderNarration calls it with the names it just
// drew; nothing else may freeze a name (§13.1(a), and see advanceNarration).
func (v *UIState) publishDrawnNames(drawn []string) {
	if v.Slugs == nil || len(drawn) == 0 {
		return
	}
	for _, id := range drawn {
		v.Slugs.Publish(id)
	}
	// Anything still pending was never drawn; it stays correctable.
	v.pendingNamed = v.pendingNamed[:0]
}
