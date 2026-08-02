package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/state"
)

// Trunk geometry (§3.2): main owns one continuous vertical line at a fixed
// column; every subagent leaves it on a curved branch at the same column; the
// last rendered row's branch terminates at its elbow.
const (
	branchMid   = "├─╮"
	branchLast  = "╰─╮"
	rowMid      = "│ ╰"
	rowLast     = "  ╰"
	contMid     = "│    "
	contLast    = "     "
	callIndent  = "  │ "
	trunkOnly   = "│ "
	trunkAbsent = "  "
)

// block is one rendered agent (or main): its styled lines plus the agent id
// each line belongs to (mouse hit-testing, §5.1).
type block struct {
	id    string
	lines [][]seg
}

// treeResult is the fitted tree region.
type treeResult struct {
	lines      [][]seg
	ids        []string // parallel to lines
	scrollInfo string   // "12–20 / 43" when the agent list is windowed
}

// renderTree renders main plus every subagent into at most avail lines,
// applying the §3.17 expansion budget (degrade 4→2→0 calls, then collapse
// pins oldest-first) and §3.9 scrolling (sticky root, windowed agents) when
// even the collapsed tree cannot fit.
func renderTree(w state.World, v *UIState, width, avail int, now time.Time, pal Palette) treeResult {
	agents := v.orderedAgents(w)
	v.assignSlugsInSpawnOrder(w.Agents)
	wide := width >= 60

	marks := map[string]bool{}
	if v.Digest != nil {
		marks = v.Digest.Marks
	}
	gutter := len(marks) > 0
	inner := width
	if gutter {
		inner = width - 1
	}

	mainLines := mainBlock(w, v, agents, inner, now, pal)
	// pre holds the lines between main and the first agent row; preIDs is the
	// selection/hit-test id of each ("" for lines that are not a row).
	var pre [][]seg
	var preIDs []string
	addPre := func(id string, ln []seg) {
		pre = append(pre, ln)
		preIDs = append(preIDs, id)
	}
	if v.DetailUnavailable != "" {
		addPre("", truncSegs(line(
			seg{"⚠ subagent detail unavailable — " + v.DetailUnavailable, pal.Settled}), inner))
	}
	if n, all := settledCount(agents); all && n > 0 {
		addPre("", truncSegs(line(
			seg{fmt.Sprintf("all clear · %d merged", n), pal.Settled}), inner))
	}
	for _, ln := range quietLines(w, v, inner, pal) {
		addPre(selQuiet, ln)
	}

	build := func(callsShown int, expanded map[string]bool, noRoom map[string]bool) []block {
		blocks := make([]block, 0, len(agents))
		for i, a := range agents {
			blocks = append(blocks, agentBlock(w, v, a, i == len(agents)-1,
				expanded[a.ID], callsShown, noRoom[a.ID], inner, wide, now, pal))
		}
		return blocks
	}
	count := func(blocks []block) int {
		n := 0
		for _, b := range blocks {
			n += len(b.lines)
		}
		return n
	}

	// Expansion set: the selected agent plus pins, minus rows that never
	// expand (§3.3).
	expanded := map[string]bool{}
	noRoom := map[string]bool{}
	if a, ok := agentByID(w, v.SelID); ok && expandable(a) {
		expanded[a.ID] = true
	}
	pinOrder := pinsOldestFirst(v)
	for _, id := range pinOrder {
		if a, ok := agentByID(w, id); ok && expandable(a) {
			expanded[a.ID] = true
		}
	}

	fixed := len(mainLines) + len(pre)
	callsShown := v.MaxCalls
	blocks := build(callsShown, expanded, noRoom)
	for _, degrade := range []int{2, 0} {
		if fixed+count(blocks) <= avail {
			break
		}
		if callsShown > degrade {
			callsShown = degrade
			blocks = build(callsShown, expanded, noRoom)
		}
	}
	// Collapse pins oldest-first (§3.17); the selected agent is never
	// degraded below activity + station.
	for _, id := range pinOrder {
		if fixed+count(blocks) <= avail {
			break
		}
		if id == v.SelID {
			continue
		}
		if expanded[id] {
			delete(expanded, id)
			noRoom[id] = true
			blocks = build(callsShown, expanded, noRoom)
		}
	}

	res := treeResult{}
	emit := func(b block) {
		for _, ln := range b.lines {
			res.lines = append(res.lines, ln)
			res.ids = append(res.ids, b.id)
		}
	}
	emitMain := func() {
		for _, ln := range mainLines {
			res.lines = append(res.lines, ln)
			res.ids = append(res.ids, event.MainAgentID)
		}
		for i, ln := range pre {
			res.lines = append(res.lines, ln)
			res.ids = append(res.ids, preIDs[i])
		}
	}

	if fixed+count(blocks) <= avail {
		emitMain()
		for _, b := range blocks {
			emit(b)
		}
	} else {
		// Window the agent list with a sticky root (§3.9 40+ agents).
		emitMain()
		budget := avail - fixed
		if budget < 0 {
			budget = 0
		}
		top, bottom := scrollWindow(blocks, agents, v, budget)
		for i := top; i < bottom; i++ {
			emit(blocks[i])
		}
		if len(agents) > 0 && bottom > top {
			res.scrollInfo = fmt.Sprintf("%d–%d / %d", top+1, bottom, len(agents))
		}
	}

	if gutter {
		for i := range res.lines {
			mark := seg{" ", pal.Primary}
			if marks[res.ids[i]] && isRowHead(res.lines[i]) {
				mark = seg{"▌", pal.NeedsYou}
			}
			res.lines[i] = append([]seg{mark}, res.lines[i]...)
		}
	}
	return res
}

// quietPreview caps how many suppressed agents the expanded list names before
// it says "+n more"; the full set is never longer than a screen's worth.
const quietPreview = 6

// quietLines renders the suppressed-agents notice: one dim line stating how
// many agents the platform announced without ever showing them do anything
// (state.World.Quiet), expanding into their ids and types when the line is
// selected.
//
// The count is the honest half of the suppression rule (§1.5): the tree does
// not carry forty rows that say nothing, and it does not pretend they never
// existed either. AGENTS n in the header counts the rendered agents, so the
// header and the tree always agree and this line reports the remainder
// separately.
func quietLines(w state.World, v *UIState, width int, pal Palette) [][]seg {
	if len(w.Quiet) == 0 {
		return nil
	}
	head := truncSegs(line(seg{quietCountText(len(w.Quiet)), pal.Settled}), width)
	if v.SelID != selQuiet {
		return [][]seg{head}
	}
	return append([][]seg{reverseLine(head)}, quietDetail(w, width, pal)...)
}

// quietCountText is the §1.5 notice itself: how many agents the platform
// announced without ever showing them do anything. EVERY screen that can be on
// display while World.Quiet is non-empty states it — the pane must never claim
// "no subagents" while the machine is holding forty of them.
func quietCountText(n int) string {
	noun := "agents"
	if n == 1 {
		noun = "agent"
	}
	return fmt.Sprintf("+%d %s with no recorded activity", n, noun)
}

// quietDetail names the suppressed agents, capped at quietPreview: the
// expansion behind the count line.
func quietDetail(w state.World, width int, pal Palette) [][]seg {
	var out [][]seg
	shown := w.Quiet
	if len(shown) > quietPreview {
		shown = shown[:quietPreview]
	}
	for _, q := range shown {
		label := placeholderName(q.ID, q.Type)
		out = append(out, truncSegs(line(
			seg{"  " + label, pal.Deep},
			seg{"  " + q.Status.String(), pal.Deep},
		), width))
	}
	if rest := len(w.Quiet) - len(shown); rest > 0 {
		out = append(out, truncSegs(line(
			seg{fmt.Sprintf("  +%d more", rest), pal.Deep}), width))
	}
	return out
}

// isRowHead reports whether a tree line is a row's first line (dot/⚑ line)
// rather than a branch or continuation — the line that takes the ▌ mark.
func isRowHead(ln []seg) bool {
	if len(ln) == 0 {
		return false
	}
	t := ln[0].text
	return t != branchMid && t != branchLast && t != contMid && t != contLast
}

// scrollWindow picks the visible block range so the selected agent stays on
// screen; the viewport otherwise holds still (§3.13).
func scrollWindow(blocks []block, agents []state.Agent, v *UIState, budget int) (top, bottom int) {
	if len(blocks) == 0 || budget <= 0 {
		return 0, 0
	}
	selIdx := -1
	for i, a := range agents {
		if a.ID == v.SelID {
			selIdx = i
		}
	}
	top = v.ScrollTop
	if top < 0 {
		top = 0
	}
	if top >= len(blocks) {
		top = len(blocks) - 1
	}
	if selIdx >= 0 && selIdx < top {
		top = selIdx
	}
	fits := func(top int) int {
		used := 0
		end := top
		for end < len(blocks) {
			n := len(blocks[end].lines)
			if used+n > budget {
				break
			}
			used += n
			end++
		}
		return end
	}
	bottom = fits(top)
	for selIdx >= 0 && selIdx >= bottom && top < selIdx {
		top++
		bottom = fits(top)
	}
	if bottom == top && top < len(blocks) {
		bottom = top + 1 // always show at least the selected block, clipped
	}
	v.ScrollTop = top
	return top, bottom
}

// mainBlock renders the root rows (§3.1): dot + main + activity + tokens,
// then the trunk-opening todo line (§2.2: real todo text, never fake pips).
func mainBlock(w state.World, v *UIState, agents []state.Agent, width int, now time.Time, pal Palette) [][]seg {
	attention := len(w.Asks) > 0
	for _, a := range agents {
		if needsYou(a) {
			attention = true
		}
	}
	dotStyle := pal.Live
	if attention {
		dotStyle = pal.NeedsYou
	}
	// The workstream label, budgeted: main's row is a label, not a prompt
	// viewer (§3.1's diagram shows "◍ main  refactor auth"). The full text
	// stays reachable in the inspector and on `y`.
	right := line(seg{formatTokens(w.Main.Tokens), pal.Settled})
	head := line(seg{"◍", dotStyle}, seg{" main", pal.Primary})
	left := head
	if task := mainLabel(w); task != "" {
		gap := 0
		if segsWidth(right) > 0 {
			gap = 1 // composeLR's minimum space before the tokens column
		}
		budget := width - segsWidth(right) - gap - segsWidth(head) - 2
		if label := budgetLabel(task, budget); label != "" {
			left = append(left, seg{"  " + label, pal.Settled})
		}
	}
	l1 := composeLR(left, right, width)
	if v.SelID == event.MainAgentID {
		l1 = reverseLine(l1)
	}
	out := [][]seg{l1}

	todo := w.Main.Todo
	if todo == "" {
		todo = w.Main.Activity
	}
	trunk := trunkOnly
	if len(agents) == 0 {
		trunk = trunkAbsent
	}
	if todo != "" {
		since := ""
		if !w.Main.LastEventAt.IsZero() {
			since = formatSince(now.Sub(w.Main.LastEventAt))
		}
		out = append(out, composeLR(
			line(seg{trunk, pal.Struct}, seg{todo, pal.Primary}),
			line(seg{since, pal.Deep}), width))
	}
	return out
}

// mainLabel is the workstream text on main's row, in priority order:
// Claude Code's own session title (short by construction), then the current
// run's prompt, then main's observed activity, then nothing. A deliberate
// refinement of §3.1/§2.2: the spec's diagram shows a short label
// ("◍ main  refactor auth"), never the raw prompt, and real prompts are whole
// sentences that swallow the row.
func mainLabel(w state.World) string {
	if w.Main.Title != "" {
		return oneLine(w.Main.Title)
	}
	if w.Run != nil && w.Run.Prompt != "" {
		return oneLine(w.Run.Prompt)
	}
	return oneLine(w.Main.Activity)
}

// oneLine flattens platform text to a single renderable row. A prompt is
// verbatim user input and is routinely multi-line (a pasted spec, a bullet
// list); newlines have zero display width, so budgetLabel would neither count
// nor cut them and the "row" would render as several physical lines, shifting
// everything below it. Control characters and ESC are dropped for the same
// reason — a stray escape would reach the terminal. Only the first non-empty
// line survives, which is the closest thing to a title the raw text offers.
// Nothing is added or reworded (C8).
func oneLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.Map(func(r rune) rune {
			if r == '\t' {
				return ' '
			}
			if r < 0x20 || r == 0x7f {
				return -1
			}
			return r
		}, ln)
		if ln = strings.TrimSpace(ln); ln != "" {
			return ln
		}
	}
	return ""
}

// budgetLabel fits s into max display columns, ending with the §3.11 ellipsis
// when anything was dropped. It cuts on a word boundary when one exists
// inside the budget and mid-word only when a single word is longer than the
// budget on its own. Width- and rune-aware throughout: titles carry em-dashes
// and non-ASCII, and a cut may never land inside a rune.
func budgetLabel(s string, max int) string {
	if max <= 0 || s == "" {
		return ""
	}
	if runewidth.StringWidth(s) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	head := truncString(s, max-1) // leave one column for the ellipsis
	if rest := s[len(head):]; rest == "" || rest[0] == ' ' {
		// The cut already landed on a word boundary — keep the last word.
		return strings.TrimRight(head, " ") + "…"
	}
	// LastIndexByte is safe on UTF-8: a 0x20 byte is never a continuation
	// byte, so slicing there can never split a rune.
	if i := strings.LastIndexByte(head, ' '); i > 0 {
		if word := strings.TrimRight(head[:i], " "); word != "" {
			return word + "…"
		}
	}
	// One word, longer than the whole budget: mid-word is the only option.
	return strings.TrimRight(head, " ") + "…"
}

// agentBlock renders one subagent per the §3.3 row anatomy.
func agentBlock(w state.World, v *UIState, a state.Agent, last, expanded bool,
	callsShown int, pinnedNoRoom bool, width int, wide bool, now time.Time, pal Palette) block {

	sl := v.slugFor(a)
	selected := a.ID == v.SelID

	branch, rowPre, contPre := branchMid, rowMid, contMid
	if last {
		branch, rowPre, contPre = branchLast, rowLast, contLast
	}

	b := block{id: a.ID}
	b.lines = append(b.lines, line(seg{branch, pal.Struct}))

	// Line 1: <dot|⚑> <slug>[  ↺n][  tail] … right side per §2.6a.
	dot, dotStyle, nameStyle := rowGlyph(a, pal)
	left := line(
		seg{rowPre, pal.Struct},
		seg{dot, dotStyle},
		seg{" " + sl, nameStyle},
	)
	if a.Retries > 0 && a.Status != state.StatusDone {
		left = append(left, seg{fmt.Sprintf("  ↺%d", a.Retries), pal.Deep})
	}
	if tail := rowTail(a, now, pinnedNoRoom); tail != "" {
		left = append(left, seg{"  " + tail, pal.Settled})
	}
	right := rowRight(a, v, wide, now, pal)
	l1 := composeLR(left, right, width)
	if selected {
		l1 = reverseLine(l1)
	}
	b.lines = append(b.lines, l1)

	if !expanded || !expandable(a) {
		return b
	}

	// Line 2 — only when the slug is lossy (§3.16): the full description.
	if !a.Teammate && v.Slugs != nil && v.Slugs.Lossy(a.ID) && a.Name != "" {
		b.lines = append(b.lines, truncSegs(line(
			seg{contPre, pal.Struct}, seg{a.Name, pal.Settled}), width))
	}

	// Line 3: ⤷ activity [✓ verified] … station pips (+label+tokens wide).
	activity := activityText(a, now)
	actStyle := pal.Primary
	if needsYou(a) {
		actStyle = pal.NeedsYou
	}
	done := a.Status == state.StatusDone
	// §3.1 degradation order on this line: the station label drops before
	// the inferred ✓ verified marker; the activity text is truncated only
	// after both are gone. Pips and tokens always survive at wide.
	actLine := func(verified, label bool) ([]seg, int) {
		left := line(
			seg{contPre, pal.Struct},
			seg{"⤷ ", pal.Settled},
			seg{activity, actStyle},
		)
		if a.Verified && verified {
			left = append(left, seg{"  ✓ verified", pal.Ok})
		}
		right := line(seg{pips(a.Station, done), pal.Deep})
		if label {
			right = append(right, seg{" " + stationLabel(a.Station, done), pal.Deep})
		}
		if wide {
			if tok := formatTokens(a.Tokens); tok != "" {
				right = append(right, seg{"  " + tok, pal.Settled})
			}
		}
		over := segsWidth(left) + segsWidth(right) + 1 - width
		return composeLR(left, right, width), over
	}
	act, over := actLine(wide, wide)
	if over > 0 && wide {
		act, over = actLine(true, false) // drop the label first (§3.1)
		if over > 0 {
			act, _ = actLine(false, false) // then the verified marker
		}
	}
	b.lines = append(b.lines, act)

	// Lines 4–7: the call-history window (§3.17). A pending ask takes the
	// final slot as the §3.1 "⚑ permission request" line.
	if callsShown > 0 {
		callBudget := callsShown
		var askLine []seg
		if age, ok := askAge(w, a.ID, now); ok {
			askLine = truncSegs(line(
				seg{contPre + callIndent, pal.Struct},
				seg{"⚑ permission request", pal.NeedsYou},
				seg{"  " + formatSince(age), pal.NeedsYou},
			), width)
			callBudget--
		}
		calls := a.CallHistory
		if len(calls) > callBudget {
			calls = calls[len(calls)-callBudget:]
		}
		hidden := a.TotalCalls - len(calls)
		for i, c := range calls {
			glyph, gs := "✔", pal.Ok
			if c.Err {
				glyph, gs = "✖", pal.NeedsYou
			}
			ln := line(
				seg{contPre + callIndent, pal.Struct},
				seg{glyph, gs},
				seg{" " + c.Tool + " " + shortPath(c.Target), pal.Settled},
			)
			if wide && c.Meta != "" {
				ln = append(ln, seg{"  " + c.Meta, pal.Deep})
			}
			if i == len(calls)-1 && hidden > 0 && askLine == nil {
				ln = append(ln, seg{fmt.Sprintf("  +%d more", hidden), pal.Deep})
			}
			b.lines = append(b.lines, truncSegs(ln, width))
		}
		if askLine != nil {
			if hidden > 0 {
				askLine = append(askLine, seg{fmt.Sprintf("  +%d more", hidden), pal.Deep})
			}
			b.lines = append(b.lines, truncSegs(askLine, width))
		}
	}
	return b
}

// rowGlyph maps status to the §2.3 dot (⚑ replaces the dot on a needs-you
// row, §3.10.2a) and the name weight.
func rowGlyph(a state.Agent, pal Palette) (dot string, dotStyle, nameStyle Style) {
	switch {
	case needsYou(a):
		return "⚑", pal.NeedsYou, pal.NeedsYou
	case a.Teammate:
		return "◇", pal.Settled, pal.Settled
	case a.Status == state.StatusQueued:
		return "○", pal.Settled, pal.Settled
	case a.Status == state.StatusDone:
		return "●", pal.Settled, pal.Settled
	case a.Status == state.StatusUnknown:
		return "◌", pal.Settled, pal.Settled
	default:
		return "●", pal.Live, pal.Primary
	}
}

// rowTail is the §3.3 collapsed tail: decay, queued, teammate, pin-overflow.
func rowTail(a state.Agent, now time.Time, pinnedNoRoom bool) string {
	switch {
	case pinnedNoRoom:
		return copyPinnedNoRoom
	case a.Decayed:
		return "returned · " + formatClock(now.Sub(a.DoneAt))
	case a.Status == state.StatusQueued:
		return copyQueuedTail
	case a.Teammate:
		return copyLimitedTele
	}
	return ""
}

// rowRight renders the §2.6a right-hand side: open call → ◐ in <Tool> + time
// in call; otherwise sparkline (wide) + since (or the t burn rate).
func rowRight(a state.Agent, v *UIState, wide bool, now time.Time, pal Palette) []seg {
	if a.Teammate || a.Status == state.StatusQueued || a.Decayed {
		return nil // no clock, no sparkline (§3.15, §3.5)
	}
	warn := needsYou(a)
	sinceStyle := pal.Deep
	if warn {
		sinceStyle = pal.NeedsYou
	}
	if showOpenCall(a, now) {
		st := pal.Live
		if warn {
			st = pal.NeedsYou
		}
		return line(
			seg{"◐ in " + a.OpenCall.Tool, st},
			seg{" " + formatSince(now.Sub(a.OpenCall.Since)), st},
		)
	}
	num := formatSince(now.Sub(a.LastEventAt))
	if v.RightCol == "rate" && a.Status != state.StatusDone {
		num = formatRate(tokensLastMinute(a.SparkBuckets))
		sinceStyle = pal.Deep
	}
	var out []seg
	if wide {
		switch {
		case a.Status == state.StatusStuck:
			out = append(out, seg{flatline, pal.NeedsYou})
		case a.Status == state.StatusDone:
			out = append(out, seg{flatline, pal.Settled})
		default:
			out = append(out, seg{sparkline(a.SparkBuckets, v.SparkMetric), pal.Live})
		}
	}
	out = append(out, seg{" " + num, sinceStyle})
	return out
}

// activityText is the ⤷ line (§3.3): honest state for ask/stuck, the observed
// activity otherwise.
func activityText(a state.Agent, now time.Time) string {
	switch a.Status {
	case state.StatusAsk:
		// §3.1: "blocked · asked to clear the TS cache" — the ask's own
		// one-line summary when the source provided one.
		if a.Ask != nil && a.Ask.Detail != "" {
			return "blocked · " + a.Ask.Detail
		}
		return "blocked · awaiting permission"
	case state.StatusStuck:
		silent := formatSince(now.Sub(a.LastEventAt))
		if a.OpenCall != nil {
			return "◐ in " + a.OpenCall.Tool + " · quiet " + silent
		}
		return "no call open · silent " + silent
	}
	if a.ActivityLine != "" {
		return a.ActivityLine
	}
	return ""
}

// askAge finds the pending ask raised by agentID (§3.7).
func askAge(w state.World, agentID string, now time.Time) (time.Duration, bool) {
	for _, ask := range w.Asks {
		if ask.AgentID == agentID {
			return now.Sub(ask.RaisedAt), true
		}
	}
	return 0, false
}

// pinsOldestFirst orders pin ids by pin sequence (§3.17 drop order).
func pinsOldestFirst(v *UIState) []string {
	ids := make([]string, 0, len(v.Pins))
	for id := range v.Pins {
		ids = append(ids, id)
	}
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && v.Pins[ids[j]] < v.Pins[ids[j-1]]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
	return ids
}

// settledCount reports how many agents are settled and whether all of them
// are (§3.9 all-clear).
func settledCount(agents []state.Agent) (done int, all bool) {
	if len(agents) == 0 {
		return 0, false
	}
	for _, a := range agents {
		switch a.Status {
		case state.StatusDone:
			done++
		case state.StatusTeammate:
		default:
			return done, false
		}
	}
	return done, true
}

// openCallDwell is how long a tool call must stay open before the row swaps
// its sparkline for the §2.6a "◐ in <Tool>" indicator.
//
// The indicator exists to answer one question: is this agent quiet because a
// long command is running, or because it is wedged? That question only arises
// once a call has been open long enough for the quiet to be ambiguous. A busy
// agent firing sub-second Bash calls would otherwise flip the right-hand
// column between the indicator and the sparkline on every frame — pure
// flicker, and §3.13 permits no animation but the spinner. Below the dwell the
// sparkline stays, which is the truer signal for rapid tool churn anyway.
const openCallDwell = 2 * time.Second

// showOpenCall reports whether the open-call indicator has earned the right
// column. There is no hysteresis to add: OpenCall is cleared when the call
// ends, and the dwell only ever delays the swap, never reverses it mid-call.
func showOpenCall(a state.Agent, now time.Time) bool {
	return a.OpenCall != nil && now.Sub(a.OpenCall.Since) >= openCallDwell
}
