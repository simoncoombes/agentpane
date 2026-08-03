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
// Three facts about this file matter more than anything in it.
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
// header alone, so turning the voice on, cycling it, writing a 200-character
// sentence — or an alert arriving — cannot move a single tree row. Putting any
// of this above the tree would move every row every time, and no amount of
// careful height accounting would fix it.
//
// **The standup IS the alert band** (§3.7, §3.19), not a region under one. Its
// sticky chip carries the band's identity and its body opens with the band's
// content, which band.go composes; there is no second fixed-height region above
// the tree, and the ask is therefore stated exactly once per frame.

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
	//
	// It is a SET. A name can stay pending for the whole run (the standup keeps
	// naming an agent the reader never scrolls to), and appending it once per
	// machine update would grow the slice for as long as the run lasts.
	for _, id := range r.Named {
		if v.pendingName(id) || (v.Slugs != nil && v.Slugs.Published(id)) {
			continue
		}
		v.pendingNamed = append(v.pendingNamed, id)
	}

	// Hold a paused view still. CommScroll counts rendered lines up from the
	// bottom, so appending entries would otherwise slide the window out from
	// under the reader; adding the new lines' height keeps the same text on
	// screen. At offset zero — auto-follow — nothing is adjusted, which is what
	// makes the newest line arrive at the bottom.
	if len(r.New) > 0 && v.CommScroll > 0 {
		v.CommScroll += commentaryTotalLines(r.New, narrate.CommandsOnScreen(w), width)
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
// narrationHeight(mode) lines, rules included, plus the row id for each line.
//
// Only the chip and the alert block carry an id (selBand): they are the band, so
// a click on them selects it the way a click on the old band region did. Prose
// carries none — §5.1 lists no such target, and a click on a sentence must not
// move the tree's selection.
func renderNarration(w state.World, v *UIState, mode VoiceMode, width int, now time.Time, pal Palette) ([][]seg, []string) {
	if mode == VoiceOff {
		return nil, nil
	}
	st := narrateNow(w, v, now)
	entries := bandEntries(w, v, now)
	out, ids, prose := renderStandup(st, entries, v, mode, width, pal)
	if mode == VoiceFull {
		comm := renderCommentary(v, narrate.CommandsOnScreen(w), width, pal)
		for _, ln := range comm {
			out, ids = append(out, ln), append(ids, "")
		}
		prose += segsLinesText(comm)
	}
	// This frame is the one the reader sees, so this is where a name becomes
	// "published" and its slug freezes (§13.1(a)). Only the names this frame's
	// PROSE actually contains are published; everything else the narrator produced
	// stays pending and correctable.
	//
	// "The region was drawn" is not the same question and was the bug: in standup
	// mode the commentary is not rendered at all, and the standup body is clipped
	// to its first few lines, so publishing the whole pending set froze six slugs
	// off one frame that showed one of them.
	//
	// The band's rows are excluded along with the tree's, for the same reason
	// renderAlertOnly publishes nothing: they print a slug from the slug table
	// exactly as a row does, on the first frame the alert exists, so treating them
	// as prose would make the guess→exact correction §13.1 keeps impossible to
	// ever apply.
	v.publishDrawnNames(w, prose)
	// Height is asserted here rather than trusted: a padding bug in either
	// region would otherwise move the footer, which is the failure §13.4 exists
	// to prevent. Clipping and padding to the declared height makes the
	// guarantee unconditional.
	want := narrationHeight(mode)
	for len(out) < want {
		out, ids = append(out, nil), append(ids, "")
	}
	return out[:want], ids[:want]
}

// renderStandup draws the standup region: rule, sticky chip, scrolling body,
// sticky action. The chip and the alert lines are the band (§3.7), so they take
// the selBand id; everything else is prose.
//
// The third return is the PROSE it drew, as plain text: the body lines that are
// sentences rather than band rows. renderNarration needs it to publish exactly
// the names this frame put in front of the reader (§13.1(a)).
func renderStandup(st narrate.Standup, entries []bandEntry, v *UIState, mode VoiceMode, width int, pal Palette) ([][]seg, []string, string) {
	out := [][]seg{rule(width, pal)}
	ids := []string{""}
	var prose strings.Builder

	alerting := bandAlerting(entries, v.Digest)
	chip, right := bandChip(st, entries, v.Digest)
	head := composeLR(
		line(seg{chip, bandChipStyle(st, entries, v.Digest, pal)}),
		line(seg{right, pal.Deep}), width)
	if alerting && v.SelID == selBand {
		head = reverseLine(head)
	}
	out, ids = append(out, head), append(ids, bandID(alerting))

	body := standupBody(st, entries, v.Digest, width, pal)
	push := func(b bodyLine) {
		id := ""
		if b.band {
			// The band's own lines, and only those: a click on a SENTENCE about
			// the ask must not move the selection, however much it is about the
			// ask. selBand still comes from selectionIDs; this is the hit test.
			id = selBand
		} else {
			prose.WriteString(segsText(b.segs))
			prose.WriteByte('\n')
		}
		out, ids = append(out, b.segs), append(ids, id)
	}
	avail := standupBodyRows(mode)
	if len(body) <= avail {
		for _, b := range body {
			push(b)
		}
		for i := len(body); i < avail; i++ {
			out, ids = append(out, nil), append(ids, "")
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
			push(b)
		}
		out, ids = append(out, truncSegs(line(seg{standupClipHint(mode, body, off, show), pal.Deep}), width)), append(ids, "")
	}

	// The sticky action. With an alert outstanding it is §3.12's exact copy for
	// the primary entry (permission or stall); otherwise it is the narrator's
	// own closing line, which §13.2 requires to always be present.
	action, tone := st.Action.Text, st.Action.Tone
	if a := bandAction(entries); a != "" {
		action, tone = a, narrate.ToneAlert
	}
	out, ids = append(out, truncSegs(line(seg{fitAction(action, width), toneStyle(tone, pal)}), width)), append(ids, "")
	return out, ids, prose.String()
}

// fitAction picks the action row's copy for the width it has to be read at.
//
// The action row is the point of the whole region (§13.2: the standup always ends
// by naming the action or saying none is needed), so it is the one line where a
// truncation is not an acceptable degradation — half a clause reads as a broken
// renderer, not as a state. Only §3.7.8's copy has a narrow form, because it is the
// only action long enough to overflow the §3.1 narrow budget; anything else falls
// through to truncSegs unchanged rather than being silently reworded.
func fitAction(s string, width int) string {
	if runewidth.StringWidth(s) <= width {
		return s
	}
	if i := strings.Index(s, narrate.ActionResolving); i >= 0 {
		if short := s[:i] + narrate.ActionResolvingNarrow + s[i+len(narrate.ActionResolving):]; runewidth.StringWidth(short) <= width {
			return short
		}
	}
	return s
}

// bandID is the row id for a band line: selBand while there is a band, "" when
// the chip is the narrator's neutral run chip and there is nothing to select.
func bandID(alerting bool) string {
	if alerting {
		return selBand
	}
	return ""
}

// alertOnlyRows is the height the compact alert block WANTS: the rule, the chip,
// the whole band body and the §3.12 action line.
//
// This is the band's degenerate form, for a pane whose tree needs every row it
// has. It is NOT a second region — it is what the standup shrinks to when the
// prose cannot be afforded, and it is drawn in the same slot below the tree, so
// the §13.4 invariant is untouched. alertOnlyMin is what it will accept: the chip
// plus the oldest entry, which names the agent, its kind and its wait.
const alertOnlyMin = 2

func alertOnlyRows(entries []bandEntry, dig *digest, width int, pal Palette) int {
	if !bandAlerting(entries, dig) {
		return 0
	}
	n := 2 + len(bandBody(entries, dig, width, pal))
	if bandAction(entries) != "" {
		n++
	}
	return n
}

// renderAlertOnly draws the compact alert block in exactly rows lines: rule,
// chip, as much of the band body as fits (oldest entry first, §3.7.6), then the
// §3.12 action line.
//
// What it gives up when it must, in order: the newer entries and the folded-ghost
// count first, then the separator rule (the region is already bracketed by the
// footer's own rule below it, so the separator is the one nicety here), then the
// action line. Never the chip, the oldest entry, or that entry's command —
// §3.7 puts "who is blocked" and "the exact command" ahead of everything else,
// and bandCoreRows is that priority written down.
//
// It deliberately does not publish names (§13.1(a)): a slug here comes from the
// slug table exactly as a tree row's does, and no prose naming the agent was
// drawn, so the guess→exact correction stays available.
func renderAlertOnly(entries []bandEntry, dig *digest, v *UIState, width, rows int, pal Palette) ([][]seg, []string) {
	if rows < alertOnlyMin || !bandAlerting(entries, dig) {
		return nil, nil
	}
	body := bandBody(entries, dig, width, pal)
	// How many ENTRIES this region can seat, which at tight heights is fewer
	// than the §3.7.6 cap. The action row may only speak for an entry inside
	// that window: bounding it by the cap let the row carry a stall's keys
	// while the stall had no row on the frame at all.
	seatable := (rows - 1) / bandRowsPerEntry
	if seatable < 1 {
		seatable = 1
	}
	action := bandActionN(entries, seatable)

	// The chip plus the oldest entry's core rows are paid for first; everything
	// else competes for what is left.
	mandatory := 1 + bandCoreRows(entries)
	if mandatory > rows {
		mandatory = rows
	}
	spare := rows - mandatory
	showAction := false
	if action != "" && spare > 0 {
		showAction, spare = true, spare-1
	}
	showRule := false
	if spare > 0 {
		showRule, spare = true, spare-1
	}
	bodyRows := mandatory - 1 + spare
	if bodyRows > len(body) {
		bodyRows = len(body)
	}

	var out [][]seg
	var ids []string
	if showRule {
		out, ids = append(out, rule(width, pal)), append(ids, "")
	}

	chip, right := bandChip(narrate.Standup{}, entries, dig)
	head := composeLR(
		line(seg{chip, bandChipStyle(narrate.Standup{}, entries, dig, pal)}),
		line(seg{right, pal.Deep}), width)
	if v.SelID == selBand {
		head = reverseLine(head)
	}
	out, ids = append(out, head), append(ids, selBand)

	for _, b := range body[:bodyRows] {
		out, ids = append(out, b.segs), append(ids, selBand)
	}
	if showAction {
		out, ids = append(out, truncSegs(line(seg{fitAction(action, width), pal.NeedsYou}), width)), append(ids, "")
	}
	for len(out) < rows {
		out, ids = append(out, nil), append(ids, "")
	}
	return out[:rows], ids[:rows]
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
// hint can say what it cut. band marks the lines that are the alert band itself
// (§3.7) rather than prose about it: they are the only ones a click may select.
type bodyLine struct {
	segs []seg
	part narrate.Part
	band bool
}

// standupBody is the scrolling middle of the region: the alert band first, then
// the narrator's sentences. Parts are not separated by blank rows: §3.1 asks for
// tight leading, and a blank row in a seven-row region costs a sentence.
//
// The band goes FIRST and the body is top-anchored, so the ask, its exact
// command and its wait timer are what the reader sees without scrolling however
// long the prose gets. §3.7 is the most important feature in the pane; the
// sentences about it are not.
func standupBody(st narrate.Standup, entries []bandEntry, dig *digest, width int, pal Palette) []bodyLine {
	out := bandBody(entries, dig, width, pal)
	for _, l := range st.Lines {
		style := toneStyle(l.Tone, pal)
		for _, wrapped := range wrapAll(l.Text, width) {
			out = append(out, bodyLine{truncSegs(line(seg{wrapped, style}), width), l.Part, false})
		}
	}
	return out
}

// renderCommentary draws the commentary region: rule, header, then the newest
// lines at the bottom.
func renderCommentary(v *UIState, onScreen map[string]bool, width int, pal Palette) [][]seg {
	out := [][]seg{rule(width, pal)}

	body, heads := commentaryLines(v.Narr.Entries, onScreen, width, pal)
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
// onScreen is the set of commands the band may print verbatim this frame
// (narrate.CommandsOnScreen). Every entry's text comes from Entry.TextOn, which is
// what keeps the log's verbatim record of an ask off a frame that is already
// showing those bytes on the band's own row — see narrate.Entry.Cmd.
func commentaryLines(entries []narrate.Entry, onScreen map[string]bool, width int, pal Palette) (lines [][]seg, heads []bool) {
	stamp := commentaryStampWidth
	indent := strings.Repeat(" ", stamp)
	textWidth := width - stamp
	if textWidth < 8 {
		textWidth = 8
	}
	for _, e := range entries {
		style := toneStyle(e.Tone, pal)
		for i, ln := range wrapAll(e.TextOn(onScreen), textWidth) {
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
//
// It measures the form that will actually be DRAWN (Entry.TextOn), because the
// two-form rule can change an entry's wrapped height: a clamp computed from the
// other form would let ⇞ run one line past the top of the log.
func commentaryTotalLines(entries []narrate.Entry, onScreen map[string]bool, width int) int {
	stamp := commentaryStampWidth
	textWidth := width - stamp
	if textWidth < 8 {
		textWidth = 8
	}
	n := 0
	for _, e := range entries {
		if k := len(wrapAll(e.TextOn(onScreen), textWidth)); k > 0 {
			n += k
		}
	}
	return n
}

// publishDrawnNames freezes the slugs of agents whose names reached a frame the
// reader could actually see, and leaves the rest pending. renderNarration calls
// it with the prose it just drew; nothing else may freeze a name (§13.1(a), and
// see advanceNarration).
//
// The gate is "this name is in that text", not "some region was drawn": a pending
// id whose slug never made it into the drawn sentences — because the commentary
// is hidden, because the standup body clipped it, or because its entry is outside
// the log's window — is still correctable, which is the whole point of tracking
// pending names at all.
func (v *UIState) publishDrawnNames(w state.World, prose string) {
	if v.Slugs == nil || len(v.pendingNamed) == 0 {
		return
	}
	// Quoted commands are removed first: a token inside the verbatim command an
	// ask quotes is a shell token, not a reference to the agent that happens to
	// share its name. narrate applies the same two rules to decide what its own
	// text names, so the two ends of the handover agree.
	text := narrate.StripQuoted(prose)
	keep := v.pendingNamed[:0]
	for _, id := range v.pendingNamed {
		if narrate.Mentions(text, v.slugOf(w, id, "")) {
			v.Slugs.Publish(id)
			continue
		}
		keep = append(keep, id)
	}
	v.pendingNamed = keep
}

// pendingName reports whether id is already waiting to be published.
func (v *UIState) pendingName(id string) bool {
	for _, p := range v.pendingNamed {
		if p == id {
			return true
		}
	}
	return false
}

// segsText is the plain text of one rendered line, styles dropped.
func segsText(segs []seg) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.t())
	}
	return b.String()
}

// segsLinesText joins rendered lines into searchable text. The newline matters:
// it is a token boundary, so a slug that ends one line and a word that starts the
// next cannot be read as one name.
func segsLinesText(lines [][]seg) string {
	var b strings.Builder
	for _, ln := range lines {
		b.WriteString(segsText(ln))
		b.WriteByte('\n')
	}
	return b.String()
}
