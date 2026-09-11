package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/state"
)

// defaultHistory is how many past tool calls sit above an agent's activity line
// by default: none.
//
// The strip was two lines per agent of what the agent had ALREADY done, and it
// cost two thirds of every block to say it. A row's job is to answer "what is
// this agent doing", and one line does that; the history is a question you ask
// of one agent at a time, and the inspector — one ⏎ or one click away — already
// holds the whole log rather than the last two lines of it. Eight agents now fit
// in the rows that used to hold three.
//
// `space` still pins a row open (§3.17): an explicit, per-row choice to keep
// max_calls_shown of its history on the tree.
const defaultHistory = 0

// growReserve is the room the growth pass (renderTree step 4) leaves unspent:
// enough rows for one more agent block to arrive — its branch, its row head
// and its activity line — so a strip that grew into a quiet pane is not torn
// back down by the very next spawn.
const growReserve = 3

// Trunk geometry (§3.2): main owns one continuous vertical line at a fixed
// column; every subagent leaves it on a curved branch at the same column; the
// last rendered row's branch terminates at its elbow.
const (
	branchMid  = "├─╮"
	branchLast = "╰─╮"
	rowMid     = "│ ╰"
	rowLast    = "  ╰"
	contMid    = "│    "
	contLast   = "     "
	histIndent = "  "
	// nowMark heads the activity line. It used to be ⤷, which pointed at
	// nothing — the line above it is not what it continues from. The strip
	// reads as one list now: ✔ done, ✔ done, ▸ doing this.
	nowMark     = "▸"
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
	lines [][]seg
	ids   []string // parallel to lines
	// foot is the returned-agents list. It is kept apart from lines because it
	// is anchored to the BOTTOM of the region rather than following the last
	// agent: finished work belongs at the foot of the pane, not floating in
	// the middle of it above a screenful of blank rows. Its height is still
	// reserved before the live agents get their budget, so anchoring it costs
	// the tree nothing it had not already given up.
	foot       [][]seg
	footIDs    []string
	scrollInfo string // "12–20 / 43" when the agent list is windowed
	// breadth reports that the breadth pass finished: every agent that can
	// expand got its activity line, so the tree is saying what everything is
	// doing and only tool-call HISTORY was rationed.
	//
	// It exists for narrationPlan (§13.2/§13.4), which must know the difference
	// between "the tree gave up call history" (fine — the mock's own budget
	// shrinks 14→8→4 as the voice grows) and "the tree gave up telling you what
	// an agent is doing" (never acceptable for prose).
	breadth bool
	// starved reports that the share the tree was given could not hold ONE whole
	// agent block, so it drew none: the frame has agents and no agent row.
	//
	// It is the tree's request for rows, and narrationPlan cedes them (see there).
	// The alternative used to be worse than scrolling: the windower emitted the
	// block anyway, the frame's last-resort clip cut it after its trunk connector,
	// and what reached the reader was a bare `├─╮` — selectable, counted in
	// `1–1 / 2`, carrying no glyph, no slug, no timer and no §3.19 change mark. A
	// frame must never claim a row it did not draw.
	starved bool
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
	// The all-clear line belongs to the view where the settled rows are ON the
	// tree. With the filter on, the list at the foot is already saying it, and
	// keying this off "nothing is hidden right now" made it blink into
	// existence for the 1.2s an agent spends being held on the tree.
	if n, all := settledCount(w.Agents); all && n > 0 && v.ShowFinished {
		addPre("", truncSegs(line(
			seg{fmt.Sprintf("all clear · %d merged", n), pal.Settled}), inner))
	}
	for _, ln := range quietLines(w, v, inner, pal) {
		addPre(selQuiet, ln)
	}
	// The returned agents are a list at the FOOT of the tree, not a notice at
	// its head: they are finished work, they belong below the work in flight,
	// and as a list they stay selectable — ⏎ opens any of them. Its rows are
	// reserved before the live agents get their expansion budget, so the list
	// is either drawn whole or degraded to its count line, never half-painted.
	returned := v.returnedAgents(w)
	postBudget := 0
	// With the filter lifted the returned agents are rows on the tree and there
	// is no list — and there was therefore nothing left to click to get back.
	// Clicking the heading toggled the reader into a state whose only exit was a
	// key they had to already know. The way back sits where the way in was.
	if v.ShowFinished && finishedTotal(w) > 0 {
		postBudget = 2
	} else if len(returned) > 0 {
		postBudget = len(returned) + 3 // a blank, the header, its rule, one line each
		if max := avail / 3; postBudget > max {
			postBudget = max // the live agents own the pane
		}
		if postBudget < 1 {
			postBudget = 1
		}
	}
	post, postIDs := returnedLines(w, v, returned, postBudget, inner, now, pal)

	// calls is the per-agent history budget: how many past tool calls sit above
	// that agent's activity line. Every agent has an entry — the strip is the
	// default row, not a reward for being selected.
	calls := map[string]int{}
	build := func(callsShown int, expanded map[string]bool, noRoom map[string]bool) []block {
		blocks := make([]block, 0, len(agents))
		for i, a := range agents {
			n := callsShown
			if c, ok := calls[a.ID]; ok && c < n {
				n = c
			}
			blocks = append(blocks, agentBlock(w, v, a, i == len(agents)-1,
				expanded[a.ID], n, noRoom[a.ID], inner, wide, now, pal))
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

	// EVERY expandable agent is expanded to start with. §3.3 expanded only the
	// selected row, and what the other rows became was a name and a number: the
	// pane could tell you eight agents were alive and not one thing any of them
	// was doing. The row is now a small strip — the last few tool calls, dimmed,
	// with what the agent is doing right now at the bottom of it — and the fit
	// ladder below takes that strip apart under pressure instead of granting it
	// as a favour.
	expanded := map[string]bool{}
	for _, a := range agents {
		if expandable(a) {
			expanded[a.ID] = true
		}
	}
	// focus is the PINNED rows: the only ones that carry history on the tree.
	// The selected row does not — selecting an agent opens the inspector, which
	// holds its whole log, and expanding it inline as well would spend rows on
	// a worse copy of what the reader is already looking at.
	focus := map[string]bool{}
	pinOrder := pinsOldestFirst(v)
	for _, id := range pinOrder {
		if a, ok := agentByID(w, id); ok && expandable(a) {
			focus[id] = true
		}
	}
	noRoom := map[string]bool{}

	fixed := len(mainLines) + len(pre) + len(post)
	callsShown := v.MaxCalls
	history := defaultHistory
	if history > callsShown {
		history = callsShown
	}
	setCalls := func() {
		for _, a := range agents {
			if focus[a.ID] {
				calls[a.ID] = callsShown
			} else {
				calls[a.ID] = history
			}
		}
	}
	setCalls()
	blocks := build(callsShown, expanded, noRoom)
	fits := func() bool { return fixed+count(blocks) <= avail }

	// Ladder, cheapest loss first (§3.17 in the strip's terms).
	//
	// 1. the shared history strip: 2 → 1 → 0. Every agent gives up the same
	//    thing at the same time, so no row is singled out and the tree keeps
	//    saying what everything is DOING for as long as it possibly can.
	for _, h := range []int{1, 0} {
		if fits() {
			break
		}
		if history > h {
			history = h
			setCalls()
			blocks = build(callsShown, expanded, noRoom)
		}
	}
	// 2. the focus rows' deeper window.
	for _, degrade := range []int{2, 0} {
		if fits() {
			break
		}
		if callsShown > degrade {
			callsShown = degrade
			setCalls()
			blocks = build(callsShown, expanded, noRoom)
		}
	}
	// 3. the activity line itself, taken from the least important rows first
	//    (autoExpandOrder ranks asks and stalls above live work above the rest,
	//    so this walks it backwards). A pinned row that loses it says so;
	//    the selected row never does.
	order := autoExpandOrder(agents)
	for i := len(order) - 1; i >= 0; i-- {
		if fits() {
			break
		}
		a := order[i]
		if a.ID == v.SelID || !expanded[a.ID] {
			continue
		}
		delete(expanded, a.ID)
		if focus[a.ID] {
			noRoom[a.ID] = true // §3.17: a pin with nowhere to go says so
		}
		blocks = build(callsShown, expanded, noRoom)
	}

	// 4. The ladder run backwards: spare rows go back into the history strip.
	//
	// The rule used to be "spare rows are spare" — a tall pane shows more
	// AGENTS, not more history about the few it already had. That is right
	// under pressure and wrong at the other end of the range, which is where
	// most sessions actually live: one subagent working alone in a
	// forty-row pane drew three lines and left thirty-five blank, and the one
	// question the pane exists to answer — what is this thing DOING — got a
	// single line of answer with a screenful of nothing under it.
	//
	// So the strip grows into rows that are otherwise going to be padding,
	// one call at a time, for every agent at once (the same evenness the
	// degrade ladder has). growReserve is what keeps it from thrashing: the
	// growth stops short of the last few rows, so the next agent to arrive
	// has somewhere to land without collapsing everybody's strip on the frame
	// it appears. It can still collapse — a fan-out of eight will take every
	// row back, which is correct — but it takes an arrival that genuinely
	// needs the space, not merely the first one.
	// Depth is never bought with breadth. If step 3 had to take an activity
	// line off any row, the tree is already short of space to say what things
	// are doing, and history — what they have already done — cannot be the
	// thing that gets rows next.
	breadthHeld := true
	for _, a := range agents {
		if expandable(a) && !expanded[a.ID] {
			breadthHeld = false
			break
		}
	}
	for breadthHeld && history < callsShown {
		try := history + 1
		prev := blocks
		history = try
		setCalls()
		blocks = build(callsShown, expanded, noRoom)
		if fixed+count(blocks)+growReserve > avail {
			history = try - 1
			setCalls()
			blocks = prev
			break
		}
	}

	// The stroke down to a new agent starts ABOVE it.
	//
	// Appending an agent does not only add a block: it re-draws the block above
	// it, whose trunk column goes from absent (it was the last one) to present.
	// That column used to flip in a single frame — the whole vertical appearing
	// at once, which is what a reader sees as "it just appeared" no matter how
	// carefully the new block below it is drawn. So the stroke travels: down
	// through the previous block, a line at a time, un-drawing its extension
	// ahead of itself, and only then into the arriving block.
	descend(w, v, agents, blocks, callsShown, calls, expanded, noRoom, inner, wide, now, pal)

	res := treeResult{breadth: true}
	for _, a := range autoExpandOrder(agents) {
		if expandable(a) && !expanded[a.ID] {
			res.breadth = false
			break
		}
	}
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

	res.foot, res.footIDs = post, postIDs

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
		// No whole block fitted. scrollInfo stays empty — "1–1 / 2" over nothing was
		// the frame claiming a row it never drew — and starved asks the caller for
		// the rows instead.
		res.starved = bottom == top && len(blocks) > 0
	}

	// The pulse: one lit cell running down the trunk from main to whichever
	// agent just reported something. It rides the trunk that is already drawn —
	// no row changes width, nothing is added to a line — so the only thing that
	// moves in the frame is where the reader's eye should go.
	if id, at, ok := treePulse(w, v); ok {
		if target := firstLineOf(res.ids, id); target > 0 {
			pos := int(at*float64(target) + 0.5)
			if pos >= len(res.lines) {
				pos = len(res.lines) - 1
			}
			if pos >= 0 && isTrunkLine(res.lines[pos]) {
				lit := pal.Live
				lit.bold = true
				res.lines[pos] = litFirstRune(res.lines[pos], lit)
			}
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
		for i := range res.foot {
			res.foot[i] = append([]seg{{" ", pal.Primary}}, res.foot[i]...)
		}
	}
	return res
}

// returnedLines renders the tree's foot: the agents that have finished, newest
// first, each one selectable so ⏎ opens the log it left behind.
//
// budget is the whole section including its header. One line means there is
// only room for the count; two or more means the header plus as many agents as
// fit, the last of them spent on "+n more" when the list is longer than the
// room. The selected agent is always among the ones drawn — a cursor may not
// sit on a row the frame did not paint.
func returnedLines(w state.World, v *UIState, returned []state.Agent, budget, width int, now time.Time, pal Palette) ([][]seg, []string) {
	if budget <= 0 {
		return nil, nil
	}
	if v.ShowFinished {
		// The rows are on the tree; this is the way back to the list.
		n := finishedTotal(w)
		if n == 0 {
			return nil, nil
		}
		ln := truncSegs(line(seg{countNoun(n, "returned agent") + " on the tree · ⏎ lists them", pal.Settled}), width)
		if v.SelID == selFinished {
			ln = reverseLine(ln)
		}
		return [][]seg{nil, ln}, []string{selFinished, selFinished}
	}
	if len(returned) == 0 {
		return nil, nil
	}
	head := func(text string) []seg {
		ln := truncSegs(line(seg{text, pal.Primary}), width)
		if v.SelID == selFinished {
			ln = reverseLine(ln)
		}
		return ln
	}
	if budget == 1 {
		return [][]seg{head(returnedCountText(len(returned)))}, []string{selFinished}
	}
	// A blank line separates finished work from work in flight, as soon as
	// there is a row to spare for it.
	var lines [][]seg
	var ids []string
	rows := budget - 1
	if budget >= 3 {
		// The blank carries the section's own id, so the frame can tell where
		// the tree ends and the list begins (and a click on it lands on the
		// header, not on nothing).
		lines, ids = append(lines, nil), append(ids, selFinished)
		rows--
	}
	more := 0
	shown := returned
	if len(shown) > rows {
		more = len(shown) - (rows - 1)
		shown = shown[:rows-1]
		// Keep the cursor on screen: a selected agent below the cut takes the
		// last drawn slot rather than disappearing out of the selection order.
		if i := agentIndex(returned, v.SelID); i >= len(shown) && len(shown) > 0 {
			shown = append(append([]state.Agent(nil), shown[:len(shown)-1]...), returned[i])
		}
	}

	lines = append(lines, head("RETURNED "+itoa(len(returned))))
	ids = append(ids, selFinished)
	// A rule under it: the list is a different region from the tree above, and
	// a heading that shares the tree's weight and colour is a heading nobody
	// reads as one.
	if rows > 1 {
		lines = append(lines, rule(width, pal))
		ids = append(ids, "")
		rows--
	}
	// Columns, not a ragged left edge: the list is a table of finished work and
	// the eye should be able to run down any one of its facts. The name and
	// model columns are as wide as the widest entry, capped so a long slug
	// cannot push the numbers off the row.
	nameW, modelW, rightW := 0, 0, 0
	for _, a := range shown {
		if n := runewidth.StringWidth(v.slugFor(a)); n > nameW {
			nameW = n // never capped: a slug is never truncated (C4)
		}
		if n := runewidth.StringWidth(agentModelText(a)); n > modelW {
			modelW = n
		}
		n := len(returnedRight(a))
		if !a.DoneAt.IsZero() && !a.SpawnedAt.IsZero() {
			n += 5
		}
		if n > rightW {
			rightW = n
		}
	}
	// The model column takes what is left after the name and the numbers, and
	// goes entirely rather than crushing either: on a narrow pane the model is
	// the one fact here the reader can get elsewhere.
	if room := width - 4 - nameW - rightW; modelW > room {
		modelW = room
	}
	if modelW < 6 {
		modelW = 0
	}

	for _, a := range shown {
		// How long it RAN, not how long ago it landed. A finished agent's row
		// was a stopwatch counting up forever, which is both the less useful
		// number and a moving one — the whole point of this list is that the
		// work in it has stopped.
		took := ""
		if !a.DoneAt.IsZero() && !a.SpawnedAt.IsZero() {
			took = formatClock(a.DoneAt.Sub(a.SpawnedAt))
		}
		// The names stay in the live accent. These agents did the work the run
		// was for; grey is what the pane says about something that never
		// reported anything, and it read like a graveyard.
		left := line(seg{"  " + pad(v.slugFor(a), nameW), pal.Live})
		if modelW > 0 {
			left = append(left, seg{"  " + pad(agentModelText(a), modelW), pal.Meta})
		}
		right := line(seg{took, pal.Settled})
		if tok := formatTokens(a.Tokens); tok != "" {
			right = append(right, seg{"  " + tok, pal.Primary})
		}
		ln := composeLR(left, right, width)
		if f, drawing := arriving(a, v); drawing {
			// It has just moved down from the tree: dim, and drawing itself in.
			ln = faintLine(ln)
			ln = drawSegs(ln, drawnCols(ln, f), pal.Live)
		}
		if a.ID == v.SelID {
			ln = reverseLine(ln)
		}
		lines = append(lines, ln)
		ids = append(ids, a.ID)
	}
	if more > 0 {
		lines = append(lines, truncSegs(line(
			seg{fmt.Sprintf("  +%d more", more), pal.Deep}), width))
		ids = append(ids, "")
	}
	return lines, ids
}

// returnedCountText is the one-line form, for the screens with no room for the
// list: the §3.9 condensed tree and a tree whose live agents have taken every
// row.
func returnedCountText(n int) string {
	return "+" + countNoun(n, "returned agent") + " · a shows them"
}

// returnedRight is the numbers on a returned row: what it cost, when it has a
// count to report.
func returnedRight(a state.Agent) string {
	return formatTokens(a.Tokens)
}

// drawnCols is how many of a line's columns the pen has reached at fraction f.
func drawnCols(ln []seg, f float64) int {
	return int(f*float64(segsWidth(ln)) + 0.5)
}

// finishedLine is that one-liner as a selectable row (the condensed screen).
func finishedLine(w state.World, v *UIState, width int, pal Palette) []seg {
	n := v.finishedHidden(w)
	if n == 0 {
		return nil
	}
	ln := truncSegs(line(seg{returnedCountText(n), pal.Settled}), width)
	if v.SelID == selFinished {
		ln = reverseLine(ln)
	}
	return ln
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

// firstLineOf is where an agent's block starts in the rendered tree, or -1 when
// it is not on screen (a scroll window can leave it off).
func firstLineOf(ids []string, id string) int {
	for i, got := range ids {
		if got == id {
			return i
		}
	}
	return -1
}

// isTrunkLine reports whether a line starts on the trunk, so the pulse only
// ever lights a trunk cell and never the first letter of a slug.
func isTrunkLine(ln []seg) bool {
	for _, s := range ln {
		if t := s.t(); t != "" {
			switch []rune(t)[0] {
			case '│', '├', '╰', '◍':
				return true
			}
			return false
		}
	}
	return false
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
	// A block that does not fit is NOT emitted clipped. It used to be ("always show
	// at least the selected block, clipped"), and what the reader got was the
	// block's trunk connector on its own after the frame's last-resort clip took the
	// rest — a selectable row with no glyph, no slug and no timer, counted in the
	// footer's `1–1 / n`. renderTree reports the starvation instead and
	// narrationPlan cedes the row.
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
	right := line(seg{formatTokens(w.Main.Tokens), pal.Primary})
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

	trunk := trunkOnly
	if len(agents) == 0 {
		trunk = trunkAbsent
	}
	if waiting := mainWaiting(w, agents); waiting != "" {
		since := ""
		if !w.Main.LastEventAt.IsZero() {
			since = formatSince(now.Sub(w.Main.LastEventAt))
		}
		out = append(out, composeLR(
			line(seg{trunk, pal.Struct}, seg{waiting, pal.Primary}),
			line(seg{since, pal.Settled}), width))
	}
	return out
}

// mainWaiting is main's line 2 (§13.3 Q7): what main is WAITING ON, in
// precedence blocked-on summary → current todo item → current activity.
//
// The todo used to lead. It cannot: TodoWrite is optional and most sessions
// never call it (platform fact 5), so leading with it left the most valuable
// line in the tree empty most of the time. Whether main is blocked is always
// knowable from the world, and it is the one thing about the root row worth a
// line of its own.
func mainWaiting(w state.World, agents []state.Agent) string {
	if s := blockedOn(w, agents); s != "" {
		return s
	}
	if w.Main.Todo != "" {
		return w.Main.Todo
	}
	// Work in flight outranks whatever main last SAID. Claude Code fires a
	// Notification hook when its own prompt goes idle — "Claude is waiting for
	// your input" — and agentpane records that text verbatim as main's
	// activity, where with a fan-out running it sat for minutes flatly
	// contradicting the tree beneath it. Main is indeed waiting: on its
	// subagents, which is the answer to the question this line asks. Once they
	// have all landed the notification is the true answer again, and it is what
	// this falls through to.
	if n := runningCount(agents); n > 0 {
		return "waiting on " + countNoun(n, "agent")
	}
	return w.Main.Activity
}

// runningCount is how many subagents are still working (stuck included: a
// stalled agent has not returned, so main is still waiting on it).
func runningCount(agents []state.Agent) int {
	n := 0
	for _, a := range agents {
		switch a.Status {
		case state.StatusRun, state.StatusStuck:
			n++
		}
	}
	return n
}

// blockedOn derives the blocked-on summary, "" when nothing is blocked. Every
// number in it is counted from the world — the line is assembled, never a
// literal (§13.2: every count is derived).
//
// "holding" names what main is sitting on: agents that exist and cannot
// progress (asking or queued). "waiting on you" is added only while an ask is
// actually outstanding, because that is the only case where the user is the
// blocker, and it goes last so the row ends on the thing to do about it.
//
// "Outstanding" excludes a RESOLVING ask (§3.7.8): its answer has already been
// seen and only the outcome is unconfirmed, so the reader is not the blocker any
// more. Counting w.Asks flat put `waiting on you` on main's row on the same frame
// the region below it said `nothing needs you — waiting on the event that
// confirms it`. The "holding" half still counts the agent, because it genuinely
// cannot progress until the outcome lands.
func blockedOn(w state.World, agents []state.Agent) string {
	held := 0
	for _, a := range agents {
		switch a.Status {
		case state.StatusAsk, state.StatusQueued:
			held++
		}
	}
	outstanding := 0
	for _, ask := range w.Asks {
		if !ask.Resolving {
			outstanding++
		}
	}
	switch {
	case outstanding > 0 && held > 0:
		return "holding " + countNoun(held, "agent") + " · waiting on you"
	case outstanding > 0:
		return "waiting on you"
	case held > 0:
		return "holding " + countNoun(held, "agent")
	}
	return ""
}

// countNoun is "1 agent" / "2 agents": derived counts read as English or they
// read as debug output.
func countNoun(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
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
		if label := promptLabel(w.Run.Prompt); label != "" {
			return label
		}
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
	return agentBlockAfter(w, v, a, last, expanded, callsShown, pinnedNoRoom, width, wide, 0, now, pal)
}

// agentBlockAfter is agentBlock with the descent delay: how long the stroke
// spends travelling down the block above before it reaches this one.
func agentBlockAfter(w state.World, v *UIState, a state.Agent, last, expanded bool,
	callsShown int, pinnedNoRoom bool, width int, wide bool, descendAfter time.Duration,
	now time.Time, pal Palette) block {

	sl := v.slugFor(a)
	selected := a.ID == v.SelID

	branch, rowPre, contPre := branchMid, rowMid, contMid
	if last {
		branch, rowPre, contPre = branchLast, rowLast, contLast
	}

	b := block{id: a.ID}
	b.lines = append(b.lines, line(seg{branch, pal.Struct}))

	// Line 1: <dot|⚑> <slug>[  ↺n][  tail] … right side per §2.6a.
	//
	// §13.3 Q3 fixes what may give way here: the slug and the right-hand number
	// are never dropped and never truncated, so when the line does not fit, the
	// gauge narrows to gaugeCellsNarrow first and only then does the collapsed
	// tail go. composeLR would otherwise cut the left side — i.e. the slug —
	// which is the one thing the row cannot lose.
	dot, dotStyle, nameStyle := rowGlyph(a, pal)
	// A row that is landing wears the accent and settles out of it, so the eye
	// is told WHICH row just appeared and not merely that the tree got taller,
	// and its dot lands as a spark: bright for the first moment, then the
	// ordinary status dot it will be for the rest of the run.
	if d, ok := entering(a, v); ok && !needsYou(a) {
		nameStyle = pal.Live
		if d < rowEnterFade {
			spark := pal.Live
			spark.bold = true
			dotStyle = spark
		}
	}
	// An unanswered ask beats. It is the one state the pane exists to shout
	// about, and at second 5 it looked exactly like it does at minute 5.
	if a.Status == state.StatusAsk && askBeat(now) {
		dotStyle = pal.Chip
	}
	headLine := func(tail bool) ([]seg, int) {
		left := line(
			seg{rowPre, pal.Struct},
			seg{dot, dotStyle},
			seg{" " + sl, nameStyle},
		)
		if a.Retries > 0 && a.Status != state.StatusDone {
			left = append(left, seg{fmt.Sprintf("  ↺%d", a.Retries), pal.Deep})
		}
		if tail {
			if t := rowTail(a, now, pinnedNoRoom); t != "" {
				left = append(left, seg{"  " + t, pal.Settled})
			}
			// What this agent is running on, on the agent's own row. The header
			// names the session's, but "which model is THIS one on" is a
			// question asked of a row, and a fan-out that mixes models — a
			// haiku explorer next to an opus writer — is exactly the case where
			// the answer per row is the whole point. It rides in the tail, so
			// the §13.3 Q3 ladder drops it before the slug or the number when
			// the row runs out of width.
			if t := agentModelText(a); t != "" {
				left = append(left, seg{"  " + t, pal.Meta})
			}
		}
		right := rowRight(w, a, v, now, pal)
		return composeLR(left, right, width), overflow(left, right, width)
	}
	l1, over := headLine(true)
	if over > 0 {
		l1, _ = headLine(false)
	}
	if selected {
		l1 = reverseLine(l1)
	}
	b.lines = append(b.lines, l1)

	if !expanded || !expandable(a) {
		// A row that carries nothing but its name still ARRIVES and still
		// LEAVES. This early return used to skip the transitions entirely,
		// which meant a new subagent never animated: the platform announces it
		// queued, expandable() is false for a queued row, and by the time its
		// first tool call made it live the arrival window had long expired. A
		// finishing agent animated fine, because a returned row IS expandable —
		// exactly the asymmetry that made this so hard to see.
		return transition(b, a, v, last, descendAfter, pal)
	}

	// The full description used to sit here whenever the slug was lossy
	// (§3.16). In practice the slug is DERIVED from the description, so the
	// two lines read as the same sentence twice — "summarize-spec…" above
	// "Summarize SPEC colour rules" — and every agent cost two rows to say
	// one thing. The description lives in the inspector (PART 4), which is
	// where §3.16 always required it; the row spends its second line on what
	// the agent is actually DOING instead. The selected row is the exception:
	// there the user has asked for this agent specifically, and the untruncated
	// name is the cheapest way to resolve a "…".
	if a.ID == v.SelID && !a.Teammate && v.Slugs != nil && v.Slugs.Lossy(a.ID) && a.Name != "" {
		b.lines = append(b.lines, truncSegs(line(
			seg{contPre, pal.Struct}, seg{a.Name, pal.Settled}), width))
	}

	// The history strip, then the activity line under it: oldest call at the
	// top, what the agent is doing RIGHT NOW at the bottom, nearest the eye
	// that just read the row's name. §3.3 had it the other way up — current
	// line first, history hanging below — which put the freshest fact of the
	// row above a growing pile of things that had already happened, and made
	// two adjacent agents' rows read as one list.
	//
	// A pending ask takes the last slot of the strip as the §3.1
	// "⚑ permission request" line: it is the most recent thing that happened,
	// and it sits directly above the "blocked · …" activity line it explains.
	activity := shortActivity(activityText(a, now))
	if callsShown > 0 {
		callBudget := callsShown
		var askLine []seg
		if age, ok := askAge(w, a.ID, now); ok {
			askLine = truncSegs(line(
				seg{contPre + histIndent, pal.Struct},
				seg{"⚑ permission request", pal.NeedsYou},
				seg{"  " + formatSince(age), pal.NeedsYou},
			), width)
			callBudget--
		}
		calls := a.CallHistory
		total := a.TotalCalls
		// The newest COMPLETED call is what the activity line reports whenever
		// nothing is open — the machine sets both from the same ToolEnd — so
		// leaving it in the strip prints one fact twice, one line apart.
		//
		// Dropping it must not shorten the strip. It used to, and since the
		// duplicate exists only while no call is OPEN, every call starting and
		// ending added and removed a line: the block changed height twice per
		// tool call and everything below it jumped. The window slides instead —
		// one more older call takes the freed slot.
		deduped := false
		if a.OpenCall == nil && len(calls) > 0 {
			last := calls[len(calls)-1]
			if shortActivity(callLabel(last)) == activity {
				calls = calls[:len(calls)-1]
				total--
				deduped = true
			}
		}
		if len(calls) > callBudget {
			calls = calls[len(calls)-callBudget:]
		}
		// The freed slot is backfilled from further back when the ring has an
		// older call to give, and held open as a blank trunk line when it does
		// not. Either way the strip is the same height whether a call is open
		// or not: it used to lose a line when one closed and gain it back when
		// the next opened, so the whole tree below moved twice per tool call.
		if deduped && len(calls) < callBudget {
			b.lines = append(b.lines, line(seg{contPre, pal.Struct}))
		}
		hidden := total - len(calls)
		for i, c := range calls {
			glyph, gs := "✔", pal.Ok
			if c.Err {
				glyph, gs = "✖", pal.NeedsYou
			}
			// Dimmer than the activity line below it: this is what the agent
			// HAS done, and it may never compete with what it is doing.
			// A step below the activity line, not two: this is what the agent
			// HAS done and it may not compete with what it is doing, but it is
			// still the record of the work and it has to be READABLE. Deep is
			// ANSI 8 with the dim attribute on top, which on a dark scheme is
			// most of the way to invisible. The call that just landed keeps
			// full weight for a moment first (anim.go).
			labelStyle := pal.Settled
			draw, fresh := flashing(a, c, v)
			if fresh {
				labelStyle = pal.Primary
			}
			ln := line(
				seg{contPre + histIndent, pal.Struct},
				seg{glyph, gs},
				seg{" " + callLabel(c), labelStyle},
			)
			if wide && c.Meta != "" {
				ln = append(ln, seg{"  " + c.Meta, pal.Deep})
			}
			// The call that just landed draws itself in, so a line appearing in
			// the middle of a strip is something you SEE happen rather than
			// something you notice happened.
			if draw < 1 {
				ln = drawSegs(ln, drawnCols(ln, draw), pal.Live)
			}
			// The elision marker rides the TOP line, which is the end the
			// calls were dropped from.
			if i == 0 && hidden > 0 {
				ln = append(ln, seg{fmt.Sprintf("  +%d more", hidden), pal.Deep})
			}
			b.lines = append(b.lines, truncSegs(ln, width))
		}
		if askLine != nil {
			if hidden > 0 && len(calls) == 0 {
				askLine = append(askLine, seg{fmt.Sprintf("  +%d more", hidden), pal.Deep})
			}
			b.lines = append(b.lines, truncSegs(askLine, width))
		}
	}

	// Last line: ▸ activity [✓ verified] … station pips (+label+tokens wide).
	//
	// An agent that never reported an activity gets NO line. It used to get one
	// anyway — a bare "▸" with the station pips and the word "returned" floating
	// at the right margin — which is a row claiming to say what an agent is doing
	// and saying nothing. Claude Code announces agents that write no transcript
	// (the same fan-out that fills World.Quiet), and the ones that record a token
	// count but never a tool call land exactly here.
	if activity == "" {
		return b
	}
	actStyle := pal.Primary
	if needsYou(a) {
		actStyle = pal.NeedsYou
	}
	done := a.Status == state.StatusDone
	// §13.3 Q3 degradation order on this line: the station LABEL goes first —
	// the pips carry the meaning and the word is a gloss — and the inferred
	// verify marker goes last, after everything else has already gone. That
	// reverses the old order, which spent the marker to keep a label.
	actLine := func(verified, wide bool) ([]seg, int) {
		left := line(
			seg{contPre, pal.Struct},
			seg{nowMark + " ", pal.Live},
			seg{activity, actStyle},
		)
		if verified {
			if text, st := verifyMarker(a, pal); text != "" {
				left = append(left, seg{"  " + text, st})
			}
		}
		// Primary: the stations are a fact about the agent and the row's only
		// statement of how far it has got. Deep is ANSI 8 with the dim
		// attribute and Settled is ANSI 8 — on a dark scheme both are most of
		// the way to invisible, which for a row's own content is no use.
		// The pips, without the word. The station label named the pip the row was
		// on — "first tool" beside ▪▫·· — on a line that is already saying what
		// the agent is doing right now. It cost ten columns of the activity text
		// to restate the mark next to it. The word survives in the inspector,
		// where there is room for a gloss.
		right := line(seg{pips(a.Station, done), pal.Primary})
		if wide {
			if tok := formatTokens(a.Tokens); tok != "" {
				right = append(right, seg{"  " + tok, pal.Primary})
			}
		}
		return composeLR(left, right, width), overflow(left, right, width)
	}
	// One rung left on this line: the failed-check marker goes, and after that
	// composeLR cuts the activity text itself. The station label used to be the
	// first thing dropped; it is no longer on the row at any width.
	act, over := actLine(true, wide)
	if over > 0 {
		act, _ = actLine(false, wide)
	}
	b.lines = append(b.lines, act)

	// Transitions (anim.go). Arriving: the trunk is drawn first and the strip
	// fills in under it a line at a time, each line dim for a moment, so the
	// block reaches its full height immediately and nothing below it moves.
	// Leaving: the whole block fades while it waits to drop into the list.
	return transition(b, a, v, last, descendAfter, pal)
}

// transition applies the arrival and departure strokes to a finished block.
//
// EVERY block goes through here — a queued row that is one line and a name, a
// teammate, a decayed row — because arriving and leaving are things that happen
// to a row, not to a row's contents.
func transition(b block, a state.Agent, v *UIState, last bool,
	descendAfter time.Duration, pal Palette) block {

	if d, ok := entering(a, v); ok {
		// The block GROWS: one line every rowDescendLine, each arriving as bare
		// trunk before anything is written on it. That is the vertical stroke
		// leading down to the new agent — and it is why the rows below move a
		// line at a time instead of being shoved down a whole block at once.
		d -= descendAfter // the stroke is still crossing the block above
		tall := enterHeight(d, len(b.lines))
		written := enterLines(d)
		b.lines = b.lines[:tall]
		for i := range b.lines {
			if i >= written {
				b.lines[i] = line(seg{trunkAt(i, last), pal.Struct})
				continue
			}
			// Fade first, then draw: the pen stays bright while the ink behind
			// it is still settling.
			if enterFading(d, i) {
				b.lines[i] = faintLine(b.lines[i])
			}
			if f := revealFrac(lineEnterAt(d, i)); f < 1 {
				b.lines[i] = drawSegs(b.lines[i], drawnCols(b.lines[i], f), pal.Live)
			}
		}
		return b
	}
	if holding(a, v) {
		left, erasing := leaving(a, v)
		for i := range b.lines {
			b.lines[i] = faintLine(b.lines[i])
			if erasing {
				// On its way down to the list, the row un-draws itself: the pen
				// runs back to the trunk and takes the line with it.
				b.lines[i] = drawSegs(b.lines[i], drawnCols(b.lines[i], left), pal.Live)
			}
		}
		// …and then the block collapses a line at a time, so what is below it
		// rises smoothly rather than jumping up by five rows in one frame.
		if n := collapseHeight(returnHoldFor-holdElapsed(a, v), len(b.lines)); n < len(b.lines) {
			b.lines = b.lines[:n]
		}
	}
	return b
}

// descend animates the trunk extension that an arriving agent causes in the
// block above it, and delays that agent's own block until the stroke arrives.
//
// It works on the built blocks rather than inside agentBlock because the change
// belongs to a DIFFERENT agent's rows: the block above is drawn with the trunk
// (it is no longer last), and reverting a line to its pre-arrival form is one
// substitution on its first rune — `├`→`╰`, `│`→` `, which is exactly what the
// last block in a tree looks like.
//
// calls is the SAME per-agent history budget build() applied, not the shared
// ceiling: rebuilding an arriving block with the ceiling made it taller than
// the height the fit ladder had just counted, and the frame's last-resort clip
// would then cut the tree. It only ever showed on an agent that reached the
// tree already carrying tool calls, which is why it went unnoticed.
func descend(w state.World, v *UIState, agents []state.Agent, blocks []block,
	callsShown int, calls map[string]int, expanded, noRoom map[string]bool,
	width int, wide bool, now time.Time, pal Palette) {

	for i := range blocks {
		d, ok := entering(agents[i], v)
		if !ok || i == 0 {
			continue
		}
		prev := blocks[i-1]
		if _, prevEntering := entering(agents[i-1], v); prevEntering {
			continue // the block above is arriving too; it carries its own stroke
		}
		// How far down the previous block the stroke has travelled. Past its
		// end the extension is complete and stays drawn — but the delay still
		// applies, or the arriving block would pop to full height the moment
		// the stroke cleared the one above it.
		for j := int(d / rowDescendLine); j < len(prev.lines); j++ {
			if j >= 0 {
				prev.lines[j] = unextendTrunk(prev.lines[j])
			}
		}
		// This agent's own block waits for the stroke to reach it.
		n := callsShown
		if c, ok := calls[agents[i].ID]; ok && c < n {
			n = c
		}
		blocks[i] = agentBlockAfter(w, v, agents[i], i == len(agents)-1,
			expanded[agents[i].ID], n, noRoom[agents[i].ID], width, wide,
			time.Duration(len(prev.lines))*rowDescendLine, now, pal)
	}
}

// unextendTrunk renders a line as it looked before the trunk reached it: the
// last block in a tree carries no vertical, so `├` is `╰` and `│` is a space.
func unextendTrunk(ln []seg) []seg {
	for i, s := range ln {
		t := s.t()
		if t == "" {
			continue
		}
		r := []rune(t)
		var head string
		switch r[0] {
		case '├':
			head = "╰"
		case '│':
			head = " "
		default:
			return ln
		}
		out := make([]seg, 0, len(ln)+1)
		out = append(out, ln[:i]...)
		out = append(out, seg{head + string(r[1:]), s.st})
		out = append(out, ln[i+1:]...)
		return out
	}
	return ln
}

// trunkAt is the bare trunk for a block line that has arrived but has nothing
// written on it yet.
//
// It is the STROKE, not the final geometry, so it is a vertical on every line
// regardless of where the block sits. That distinction was the whole bug: a
// newly arrived agent is by definition the LAST one, the last block's
// continuation prefix is five spaces (§3.2: no stub below the elbow), and so
// every line of the descent drew nothing at all. The pane was animating a
// vertical made of blanks, for a third of a second, and then the row appeared —
// which is exactly what "it just appears" looks like.
//
// The elbow forms when the line is written, and §3.2 is satisfied by the
// finished frame, which is what it is about: a stroke in flight is not a stub.
func trunkAt(int, bool) string {
	return trunkOnly
}

// holdElapsed is how long a returning block has been held.
func holdElapsed(a state.Agent, v *UIState) time.Duration {
	d, _ := v.marked(doneKey(a.ID))
	return d
}

// overflow reports how many columns a left/right pair is over width, counting
// the gap composeLR inserts between them (and only when there is a right side
// to separate). Positive means the degradation ladder has to give something up;
// zero or less means the line fits as built.
func overflow(left, right []seg, width int) int {
	gap := 0
	if segsWidth(right) > 0 {
		gap = 1
	}
	return segsWidth(left) + segsWidth(right) + gap - width
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

// agentModelText is "opus-5 · high" for an agent's row: what it is running on
// and at what effort, both verbatim from its own transcript (§C8 — nothing is
// inherited from the session and nothing is invented; an agent the pane was
// never told about says nothing).
func agentModelText(a state.Agent) string {
	if a.Model == "" {
		return a.Effort
	}
	out := shortModel(a.Model)
	if a.Effort != "" {
		out += " · " + a.Effort
	}
	return out
}

// rowTail is the §3.3 collapsed tail: returned, queued, teammate,
// pin-overflow. "returned" is the word for a subagent at every age (§13.3 Q4);
// only main merges, and §3.12's `merged · <mm:ss>` is withdrawn.
func rowTail(a state.Agent, now time.Time, pinnedNoRoom bool) string {
	switch {
	case pinnedNoRoom:
		return copyPinnedNoRoom
	case a.Status == state.StatusDone:
		// The right column shows nothing for a settled row (§13.3 Q2), so the
		// tail carries how long ago it landed — for the whole time it is on the
		// tree, not just after it decays.
		if a.DoneAt.IsZero() {
			return "returned"
		}
		return "returned · " + formatClock(now.Sub(a.DoneAt))
	case a.Status == state.StatusQueued:
		return copyQueuedTail
	case a.Teammate:
		return copyLimitedTele
	}
	return ""
}

// verifyMarker is the §13.3 Q6 inferred marker, split on the exit code: a zero
// exit reads "✓ verified", a non-zero one "✖ verify failed", and a check still
// running says nothing at all (state.VerifyNone) because there is no outcome to
// report yet. It lives on the activity line and never on the pips — a station
// means "this is a fact" and an inference may not sit among them.
func verifyMarker(a state.Agent, pal Palette) (string, Style) {
	if a.Verify == state.VerifyFailed {
		return "✖ verify failed", pal.NeedsYou
	}
	// A check that PASSED says nothing the reader has to act on, and it is only
	// ever an inference — a command whose name looked like a check exited zero.
	// It cost row width to tell you the expected thing happened, next to a ✓
	// that reads exactly like the ✔ a successful tool call gets. A check that
	// FAILED is news, so that one stays. Both are in the inspector, labelled
	// (inferred), which is where a claim like this belongs in full.
	return "", pal.Settled
}

// settledRow reports the rows that show neither a gauge nor a number (§13.3
// Q2): a returned agent, a queued one, a teammate. Nothing about them is
// changing, so a moving number would be noise and a bar would be a memory.
func settledRow(a state.Agent) bool {
	return a.Teammate || a.Decayed ||
		a.Status == state.StatusQueued || a.Status == state.StatusDone
}

// rowRight renders the right-hand side under the §13.3 Q2 rule:
//
//	settled          nothing at all
//	in a call ≥2s    ◐ in <Tool> + how long the call has been open
//	stuck | ask      how long it has been SILENT
//	otherwise        the burn rate (or the clock, behind `t`)
//
// The stuck/ask override is not toggleable, and `t` cannot reach it: silence is
// the entire point of the number there.
//
// The activity gauge that used to precede the number is gone. It was five cells
// of accent block on every live row — the loudest thing in the pane, repeated
// once per agent — and what it bought over the exact number beside it was a
// coarse comparison the reader can make from the numbers themselves. The
// inspector still plots an agent's activity across the whole run.
func rowRight(w state.World, a state.Agent, v *UIState, now time.Time, pal Palette) []seg {
	if settledRow(a) {
		return nil // no clock, no number (§3.15, §3.5, §13.3 Q2)
	}
	if showOpenCall(a, now) {
		st := pal.Live
		if needsYou(a) {
			st = pal.NeedsYou
		}
		return line(
			seg{"◐ in " + a.OpenCall.Tool, st},
			seg{" " + formatSince(now.Sub(a.OpenCall.Since)), st},
		)
	}
	num, numStyle := rowNumber(a, v, now, pal)
	// How long you have kept it waiting, as pressure rather than as a number
	// you have to compare against a threshold you cannot see. Asks only: this
	// is the one row where the reader is the blocker.
	if a.Status == state.StatusAsk {
		if waited, ok := askAge(w, a.ID, now); ok {
			return line(
				seg{askPressureBar(waited, v.NotifyAfter), pal.NeedsYou},
				seg{" " + num, numStyle},
			)
		}
	}
	return line(seg{num, numStyle})
}

// askPressureBar renders the §3.20 escalation as a bar: full when the pane has
// run out of quieter ways to tell you.
func askPressureBar(waited, full time.Duration) string {
	filled := int(askPressure(waited, full)*float64(askPressureCells) + 0.5)
	if filled < 1 {
		filled = 1 // a raised ask always shows something
	}
	if filled > askPressureCells {
		filled = askPressureCells
	}
	return strings.Repeat(gaugeFilled, filled) + strings.Repeat(gaugeEmpty, askPressureCells-filled)
}

// rowNumber picks the number on the right of a live row (§13.3 Q2).
//
// A stalled row never shows `0.0k/s`: it is technically true and completely
// mute, and the thing worth knowing about a row that has stopped is how long it
// has been stopped. That covers ask and stuck by status, unknown (silent past
// unknown_after, which is stalled by any honest reading), and any row whose
// burn rate has decayed to nothing — a zero rate answers the question the rate
// column exists to answer with silence.
func rowNumber(a state.Agent, v *UIState, now time.Time, pal Palette) (string, Style) {
	silence := formatSince(now.Sub(a.LastEventAt))
	if needsYou(a) {
		return silence, pal.NeedsYou
	}
	// Primary, not chrome: the rate and the silence are the row's measurement,
	// and dim-grey-on-dark made the one number on the row the hardest thing on
	// it to read.
	if a.Status == state.StatusUnknown || v.RightCol != "rate" {
		return silence, pal.Primary
	}
	// The test is what the column would SAY, not whether the underlying number
	// is exactly zero: a trickle that rounds to 0.0k/s is just as mute as no
	// tokens at all, and §13.4 forbids that reading on a row that has stopped.
	if rate := formatRate(tokensLastMinute(a.SparkBuckets)); rate != mutedRate {
		return rate, pal.Primary
	}
	return silence, pal.Primary
}

// mutedRate is the one rendering of the burn rate that answers nothing.
const mutedRate = "0.0k/s"

// activityText is the ▸ line (§3.3): honest state for ask/stuck, the observed
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

// autoExpandOrder ranks agents for the spare-room expansion: the ones whose
// activity the user most needs first. Attention states lead, then live work,
// then everything else in render order. Settled and never-expandable rows are
// left to the caller's expandable() check.
func autoExpandOrder(agents []state.Agent) []state.Agent {
	rank := func(a state.Agent) int {
		switch a.Status {
		case state.StatusAsk, state.StatusStuck:
			return 0
		case state.StatusRun:
			return 1
		default:
			return 2
		}
	}
	out := append([]state.Agent(nil), agents...)
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	return out
}

// callDepths is the ladder the depth pass tries, deepest first: give an agent
// its whole history if the pane can hold it, else a couple of calls, else one.
func callDepths(max int) []int {
	out := make([]int, 0, 3)
	for _, n := range []int{max, 2, 1} {
		if n > 0 && n <= max && (len(out) == 0 || n < out[len(out)-1]) {
			out = append(out, n)
		}
	}
	return out
}
