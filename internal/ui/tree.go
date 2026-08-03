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
	// breadth reports that the breadth pass finished: every agent that can
	// expand got its activity line, so the tree is saying what everything is
	// doing and only tool-call HISTORY was rationed.
	//
	// It exists for narrationPlan (§13.2/§13.4), which must know the difference
	// between "the tree gave up call history" (fine — the mock's own budget
	// shrinks 14→8→4 as the voice grows) and "the tree gave up telling you what
	// an agent is doing" (never acceptable for prose).
	breadth bool
}

// renderTree renders main plus every subagent into at most avail lines,
// applying the §3.17 expansion budget (degrade 4→2→0 calls, then collapse
// pins oldest-first) and §3.9 scrolling (sticky root, windowed agents) when
// even the collapsed tree cannot fit.
func renderTree(w state.World, v *UIState, width, avail int, now time.Time, pal Palette) treeResult {
	agents := v.orderedAgents(w)
	v.assignSlugsInSpawnOrder(w.Agents)
	wide := width >= 60
	// One denominator for every row: bars only mean something if they share a
	// scale (gauge.go).
	runMax := runActivityMax(agents, v.SparkMetric)

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

	// calls is the per-agent tool-call budget; anything not listed gets the
	// default. Auto-expanded rows (below) take 0 so breadth costs one line
	// each, while the selected agent keeps its history.
	calls := map[string]int{}
	build := func(callsShown int, expanded map[string]bool, noRoom map[string]bool) []block {
		blocks := make([]block, 0, len(agents))
		for i, a := range agents {
			n := callsShown
			if c, ok := calls[a.ID]; ok && c < n {
				n = c
			}
			blocks = append(blocks, agentBlock(w, v, a, i == len(agents)-1,
				expanded[a.ID], runMax, n, noRoom[a.ID], inner, wide, now, pal))
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

	// §3.3 makes the SELECTED agent expand; it never said the pane should sit
	// two-thirds empty while every other agent hides what it is doing. When
	// rows remain after the budget above, spend them on activity lines —
	// breadth first (one line per agent, no call history), attention and live
	// agents before settled ones. Each candidate is trial-fitted, so this can
	// never push the tree into scrolling; it only ever consumes slack.
	for _, a := range autoExpandOrder(agents) {
		if fixed+count(blocks) >= avail {
			break
		}
		if expanded[a.ID] || noRoom[a.ID] || !expandable(a) {
			continue
		}
		trial := make(map[string]bool, len(expanded)+1)
		for k, v := range expanded {
			trial[k] = v
		}
		trial[a.ID] = true
		prevCalls := calls[a.ID]
		hadCalls := false
		if _, ok := calls[a.ID]; ok {
			hadCalls = true
		}
		calls[a.ID] = 0
		nb := build(callsShown, trial, noRoom)
		if fixed+count(nb) <= avail {
			expanded, blocks = trial, nb
			continue
		}
		if hadCalls {
			calls[a.ID] = prevCalls
		} else {
			delete(calls, a.ID)
		}
		break
	}

	// Depth pass. The breadth pass above gives every agent it can reach ONE
	// line (its activity) so the tree says what everything is doing. §3.3 also
	// puts up to max_calls_shown tool calls under an expanded agent, and rows
	// left over after breadth are best spent on that history — most-important
	// agent first, and never so much that the tree starts scrolling.
	for _, a := range autoExpandOrder(agents) {
		if fixed+count(blocks) >= avail {
			break
		}
		if !expanded[a.ID] {
			continue
		}
		if c, ok := calls[a.ID]; !ok || c >= callsShown {
			continue // already drawing its full history
		}
		grew := false
		for _, want := range callDepths(callsShown) {
			if want <= calls[a.ID] {
				continue
			}
			prev := calls[a.ID]
			calls[a.ID] = want
			nb := build(callsShown, expanded, noRoom)
			if fixed+count(nb) <= avail {
				blocks, grew = nb, true
				break
			}
			calls[a.ID] = prev
		}
		if !grew {
			break // nothing fits for this agent, so nothing will for the rest
		}
	}

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
			line(seg{since, pal.Deep}), width))
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
	return w.Main.Activity
}

// blockedOn derives the blocked-on summary, "" when nothing is blocked. Every
// number in it is counted from the world — the line is assembled, never a
// literal (§13.2: every count is derived).
//
// "holding" names what main is sitting on: agents that exist and cannot
// progress (asking or queued). "waiting on you" is added only while an ask is
// actually outstanding, because that is the only case where the user is the
// blocker, and it goes last so the row ends on the thing to do about it.
func blockedOn(w state.World, agents []state.Agent) string {
	held := 0
	for _, a := range agents {
		switch a.Status {
		case state.StatusAsk, state.StatusQueued:
			held++
		}
	}
	switch {
	case len(w.Asks) > 0 && held > 0:
		return "holding " + countNoun(held, "agent") + " · waiting on you"
	case len(w.Asks) > 0:
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
func agentBlock(w state.World, v *UIState, a state.Agent, last, expanded bool, runMax int,
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
	//
	// §13.3 Q3 fixes what may give way here: the slug and the right-hand number
	// are never dropped and never truncated, so when the line does not fit, the
	// gauge narrows to gaugeCellsNarrow first and only then does the collapsed
	// tail go. composeLR would otherwise cut the left side — i.e. the slug —
	// which is the one thing the row cannot lose.
	dot, dotStyle, nameStyle := rowGlyph(a, pal)
	headLine := func(cells int, tail bool) ([]seg, int) {
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
		}
		right := rowRight(a, v, cells, runMax, now, pal)
		return composeLR(left, right, width), overflow(left, right, width)
	}
	l1, over := headLine(gaugeCells, true)
	if over > 0 {
		if l1, over = headLine(gaugeCellsNarrow, true); over > 0 {
			l1, _ = headLine(gaugeCellsNarrow, false)
		}
	}
	if selected {
		l1 = reverseLine(l1)
	}
	b.lines = append(b.lines, l1)

	if !expanded || !expandable(a) {
		return b
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

	// Line 3: ⤷ activity [✓ verified] … station pips (+label+tokens wide).
	activity := activityText(a, now)
	actStyle := pal.Primary
	if needsYou(a) {
		actStyle = pal.NeedsYou
	}
	done := a.Status == state.StatusDone
	// §13.3 Q3 degradation order on this line: the station LABEL goes first —
	// the pips carry the meaning and the word is a gloss — and the inferred
	// verify marker goes last, after everything else has already gone. That
	// reverses the old order, which spent the marker to keep a label.
	actLine := func(verified, label bool) ([]seg, int) {
		left := line(
			seg{contPre, pal.Struct},
			seg{"⤷ ", pal.Settled},
			seg{activity, actStyle},
		)
		if verified {
			if text, st := verifyMarker(a, pal); text != "" {
				left = append(left, seg{"  " + text, st})
			}
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
		return composeLR(left, right, width), overflow(left, right, width)
	}
	act, over := actLine(true, wide)
	if over > 0 {
		act, over = actLine(true, false) // the station label first (§13.3 Q3)
		if over > 0 {
			act, _ = actLine(false, false) // the verify marker last
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
	switch a.Verify {
	case state.VerifyOK:
		return "✓ verified", pal.Ok
	case state.VerifyFailed:
		return "✖ verify failed", pal.NeedsYou
	}
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
//	stuck | ask      the gauge + how long it has been SILENT
//	otherwise        the gauge + the burn rate (or the clock, behind `t`)
//
// The stuck/ask override is not toggleable, and `t` cannot reach it: silence is
// the entire point of the number there. runMax is the busiest live agent's
// level, the shared denominator that makes one row's bar mean the same as
// another's (gauge.go); cells is the gauge width the caller's fit ladder settled
// on.
func rowRight(a state.Agent, v *UIState, cells, runMax int, now time.Time, pal Palette) []seg {
	if settledRow(a) {
		return nil // no clock, no gauge (§3.15, §3.5, §13.3 Q2)
	}
	warn := needsYou(a)
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
	num, numStyle := rowNumber(a, v, now, pal)
	gaugeStyle := pal.Live
	if warn {
		gaugeStyle = pal.NeedsYou
	}
	return line(
		seg{gaugeFor(a, v.SparkMetric, runMax, cells, now), gaugeStyle},
		seg{" " + num, numStyle},
	)
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
	if a.Status == state.StatusUnknown || v.RightCol != "rate" {
		return silence, pal.Deep
	}
	// The test is what the column would SAY, not whether the underlying number
	// is exactly zero: a trickle that rounds to 0.0k/s is just as mute as no
	// tokens at all, and §13.4 forbids that reading on a row that has stopped.
	if rate := formatRate(tokensLastMinute(a.SparkBuckets)); rate != mutedRate {
		return rate, pal.Deep
	}
	return silence, pal.Deep
}

// mutedRate is the one rendering of the burn rate that answers nothing.
const mutedRate = "0.0k/s"

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
