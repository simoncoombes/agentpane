package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/state"
)

// frameMeta maps rendered lines back to agent ids for mouse hit-testing
// (§5.1: a click selects a row and opens it).
type frameMeta struct {
	lineIDs []string // one entry per rendered line; "" = not a row
}

// renderFrame is the pure render core: the full frame from a world snapshot,
// view state, geometry, an injected clock, and a palette. No I/O, no
// time.Now() — this is what the snapshot tests call.
func renderFrame(w state.World, v *UIState, cols, rows int, now time.Time, pal Palette) (string, *frameMeta) {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	// The frame's clock, where the parts of the view that are not handed a
	// `now` can reach it — UIState.live holds a just-returned agent on the tree
	// for a moment before it drops into the list at the foot.
	v.Clock = now
	if v.Overlay {
		return finish(renderOverlay(w, v, cols, rows, pal), nil, cols, rows)
	}
	if cols < 40 {
		return finishMeta(renderTiny(w, v, cols, rows, now, pal), cols, rows)
	}
	width := frameWidth(v, cols)
	if idlePhase(w) {
		return finishMeta(renderIdleFrame(w, v, width, rows, now, pal), width, rows)
	}
	if rows < 12 {
		return finishMeta(renderCondensed(w, v, width, rows, now, pal), width, rows)
	}
	return finishMeta(renderActive(w, v, width, rows, now, pal), width, rows)
}

// Column budgets for the three §3.1 layouts. wide-64 and narrow-44 are the
// spec's; fill is the addition below.
const (
	narrowColumns = 44
	wideColumns   = 64
	// fillMaxColumns is where fill stops by default. A pane can be dragged to
	// 200 columns, and a row of text that wide is not read, it is scanned
	// twice, so fill takes the pane it is given up to a limit that keeps one
	// row one eyeful. `max_width` in config.toml moves it.
	fillMaxColumns = 100
)

// frameWidth is the rendered width for a pane of cols columns.
//
// fill is the default and needs no switching on: the pane draws at whatever
// width it has been given, up to v.MaxWidth. Everything above 64 columns goes
// to text, because the activity line, the tool-call labels and main's
// workstream label are all budgeted against this number and simply stop being
// truncated.
//
// wide and narrow are the FIXED budgets the tree was originally drawn
// against, still reachable with `w`. They are a ceiling, never a floor, so
// narrowing a pane below the budget reflows in every mode; only widening past
// it is what they refuse.
func frameWidth(v *UIState, cols int) int {
	width := cols
	switch v.WidthMode {
	case "narrow":
		if width > narrowColumns {
			return narrowColumns
		}
	case "wide":
		if width > wideColumns {
			return wideColumns
		}
	default:
		max := v.MaxWidth
		if max < narrowColumns {
			max = fillMaxColumns
		}
		if width > max {
			return max
		}
	}
	return width
}

// quietLineDrawn reports whether the current geometry paints the §1.5
// suppressed-agents notice as a selectable row. Every screen does — the tree,
// the idle screen and the condensed screen — except the <40-column one, where
// the count appears on the header instead (renderTiny).
func quietLineDrawn(w state.World, cols int) bool {
	return len(w.Quiet) > 0 && cols >= 40
}

// idlePhase reports whether the §3.8 idle screen owns the pane: no run in
// flight and the session is idle/ended, or nothing has ever happened.
func idlePhase(w state.World) bool {
	if w.Run != nil {
		return false
	}
	switch w.Session.State {
	case state.SessionIdle, state.SessionEnded:
		return true
	}
	return len(w.Agents) == 0
}

type frame struct {
	lines [][]seg
	ids   []string
}

func (f *frame) add(id string, ln []seg) {
	f.lines = append(f.lines, ln)
	f.ids = append(f.ids, id)
}

func (f *frame) addAll(id string, lns [][]seg) {
	for _, ln := range lns {
		f.add(id, ln)
	}
}

func rule(width int, pal Palette) []seg {
	return line(seg{strings.Repeat("─", width), pal.Struct})
}

// renderActive renders the live tree screen (§3.1).
//
// Three regions: header, tree, and the inspector when it is open. Nothing
// fixed-height sits above the tree, so the tree's first row is always y=2 —
// that is the §13.4 invariant, held by construction rather than by arithmetic.
//
// Nothing fixed-height sits BELOW it either any more. The alert band, the
// standup, the commentary and the §2.7 contention block have all been removed,
// so the tree owns every row between the header and the inspector. What used to
// be a negotiation over the leftover rows (narrationPlan) is now a subtraction:
// the tree gets what the chrome and the inspector do not take.
//
// The alerts themselves are not gone, only their region: the header still counts
// them (`⚑ n NEEDS YOU`) and the tree still marks the agents they belong to.
func renderActive(w state.World, v *UIState, width, rows int, now time.Time, pal Palette) *frame {
	f := &frame{}
	entries := bandEntries(w, v, now)

	f.add("", headerLine(w, v, width, now, pal, bandAlertCount(entries)))
	f.add("", rule(width, pal))

	var inspLines [][]seg
	if v.InspectorOpen {
		inspLines = renderInspector(w, v, width, rows, now, pal)
	}

	// chrome is the header and the footer with their rules: the two rows at each
	// end that every active frame owns unconditionally.
	const chrome = 4
	avail := rows - chrome - len(inspLines)
	if avail < minTreeRows {
		avail = minTreeRows
	}
	tree := renderTree(w, v, width, avail, now, pal)

	// Last-resort clips, in priority order. renderTree cannot go below main's own
	// block plus one agent's, and renderInspector has a fixed height of its own, so
	// on a very short pane either can hand back more lines than the budget it was
	// given — and the excess used to push the footer, which carries the toasts and
	// every key hint, straight off the bottom of the frame.
	//
	// The tree gives up rows first, then the inspector; the footer never does,
	// because a pane whose keys have scrolled away is a pane you cannot get out of.
	treeLines, treeIDs := tree.lines, tree.ids
	rest := rows - chrome
	if rest < 0 {
		rest = 0
	}
	if len(treeLines) > rest {
		cut := treeBlockCut(treeIDs, rest)
		treeLines, treeIDs = treeLines[:cut], treeIDs[:cut]
	}
	if rest -= len(treeLines); len(inspLines) > rest {
		inspLines = inspLines[:rest]
	}
	for i, ln := range treeLines {
		f.add(treeIDs[i], ln)
	}

	// Pad so the returned list, the inspector and the footer sit at the bottom.
	// The list is anchored rather than trailing the last agent: it is finished
	// work, and finished work belongs at the foot of the pane and not floating
	// in the middle of it with blank rows underneath.
	foot, footIDs := tree.foot, tree.footIDs
	if rest -= len(inspLines); len(foot) > rest {
		foot, footIDs = nil, nil
	}
	pad := rows - len(f.lines) - len(foot) - len(inspLines) - 2
	for i := 0; i < pad; i++ {
		f.add("", nil)
	}
	for i, ln := range foot {
		f.add(footIDs[i], ln)
	}
	f.addAll(v.SelID, inspLines)
	f.add("", rule(width, pal))
	f.add("", footerLine(w, v, width, now, pal, tree.scrollInfo))

	if !w.Connected {
		// §3.9: tree dimmed 50% via the dim attribute, keys still work.
		for i := 2; i < len(f.lines)-2; i++ {
			f.lines[i] = faintLine(f.lines[i])
		}
	}
	return f
}

// minTreeRows is the floor the frame leaves the tree: main's own two rows plus
// one. A pane too short to honour it draws a clipped tree rather than none.
const minTreeRows = 3

// treeBlockCut is where the tree's lines may be cut so that no agent's block is
// left half-drawn: at most `rest` lines, backing off to the start of whatever
// block the cut landed inside.
//
// A block is a maximal run of one id in the hit-test metadata, so this needs no
// knowledge of what a row looks like. It is the frame's own restatement of the
// rule renderTree now holds internally (treeResult.starved): cutting a block after
// its trunk connector leaves `├─╮` standing in for an agent — a selectable row with
// no glyph, no slug, no timer and no §3.19 change mark. A frame must never claim a
// row it did not draw.
func treeBlockCut(ids []string, rest int) int {
	if rest <= 0 {
		return 0
	}
	if rest >= len(ids) {
		return len(ids)
	}
	cut := rest
	for cut > 0 && ids[cut] != "" && ids[cut] == ids[cut-1] {
		cut--
	}
	if cut == 0 {
		// Only main's block can start at line 0 (emitMain always runs first), and
		// main's row head IS its first line, so clipping it keeps the row it claims.
		// Backing off to nothing would drop the sticky root instead, which is worse
		// than either.
		return rest
	}
	return cut
}

// headerSessionID is the "AGENTS n · <id>" suffix (§3.8a): which session this
// pane is showing. It appears whenever that is a real question — several live
// sessions to confuse it with, or a pinned pane, where naming the session the
// pane belongs to is the entire point of pinning it and stays true even when
// it is the only one running. A pinned pane whose session has not reached the
// registry yet has no world id; it names the pin, which is the honest answer
// to "what am I looking at".
func headerSessionID(w state.World, v *UIState) string {
	if w.Session.ShortID != "" && (v.PinnedSession != "" || len(v.Sessions) > 1) {
		return w.Session.ShortID
	}
	if v.PinnedSession != "" {
		return shortSessionID(v.PinnedSession)
	}
	return ""
}

// headerLine is §3.1: "AGENTS n[ · id]" left, attention/status right.
func headerLine(w state.World, v *UIState, width int, now time.Time, pal Palette, alerts int) []seg {
	// The count is the rows on the TREE, not every agent the session has ever
	// had. A header reading 8 above three rows and a RETURNED list reconciles
	// only if you do the arithmetic, and it agrees with nothing else on the
	// machine — Claude Code's own footer lists the agents that are working.
	// Everything else is still accounted for on screen: the returned list
	// counts its own, and the quiet line counts the suppressed.
	left := line(seg{fmt.Sprintf("AGENTS %d", len(v.orderedAgents(w))), pal.Primary})
	if id := headerSessionID(w, v); id != "" {
		left = append(left, seg{" · " + id, pal.Settled})
	}
	// What the session is running on. It belongs here and not on every row:
	// the answer is the same for every agent except the ones spawned onto
	// something else, and those say so on their own row instead (agentBlock).
	if width >= 56 {
		if m := sessionModelText(w); m != "" {
			left = append(left, seg{" · " + m, pal.Meta})
		}
	}
	// How much of the fan-out has landed. The header counts the agents; this
	// says how far through them the run is, which is the question anyone
	// watching a fan-out is actually asking.
	if width >= 60 {
		if bar, text := runProgress(w); bar != "" {
			left = append(left,
				seg{"  " + bar, pal.Live},
				seg{" " + text, pal.Settled})
		}
	}
	var right []seg
	switch {
	case !w.Connected:
		right = line(seg{copyDisconnected, pal.NeedsYou})
	case alerts > 0:
		right = line(seg{fmt.Sprintf("⚑ %d NEEDS YOU", alerts), pal.NeedsYou})
	default:
		// The clock is the run's — it is what "how long has this been going"
		// means — but the token count is the whole session's (World.Tokens),
		// because everything it sits above is. It is also the only number here
		// worth having when no run is in flight, which is why it is no longer
		// gated on one.
		var parts []string
		if w.Run != nil {
			parts = append(parts, formatClock(now.Sub(w.Run.StartedAt)))
		}
		if tok := formatTokens(w.Tokens()); tok != "" {
			parts = append(parts, tok+" tok")
		}
		if len(parts) > 0 {
			right = line(seg{strings.Join(parts, " · "), pal.Settled})
		}
	}
	return composeLR(left, right, width)
}

// runProgressCells is the width of the header's progress bar.
const runProgressCells = 6

// runProgress is the header's "▇▇▇··· 3/8": how many subagents have returned
// out of how many exist. Teammates are excluded — they are peers, not work this
// run is waiting on — and a run with nothing to count reports nothing.
func runProgress(w state.World) (bar, text string) {
	total, done := 0, 0
	for _, a := range w.Agents {
		if a.Teammate {
			continue
		}
		total++
		if a.Status == state.StatusDone {
			done++
		}
	}
	if total == 0 {
		return "", ""
	}
	filled := done * runProgressCells / total
	if done > 0 && filled == 0 {
		filled = 1 // work that landed never reads as none (C8)
	}
	return strings.Repeat(gaugeFilled, filled) + strings.Repeat(gaugeEmpty, runProgressCells-filled),
		fmt.Sprintf("%d/%d", done, total)
}

// sessionModelText is "opus-5 · high" for the header, "" when the pane was
// never told (hooks carry neither field, so a hooks-only pane says nothing).
func sessionModelText(w state.World) string {
	if w.Main.Model == "" {
		return ""
	}
	out := shortModel(w.Main.Model)
	if w.Main.Effort != "" {
		out += " · " + w.Main.Effort
	}
	return out
}

// footerLine is the §3.1 footer: toasts/hints left, mode hints right (§5.2).
func footerLine(w state.World, v *UIState, width int, now time.Time, pal Palette, scrollInfo string) []seg {
	var left []seg
	switch {
	case v.Toast.Text != "" && now.Sub(v.Toast.At) < toastFor:
		st := pal.Live
		prefix := "✓ "
		if v.Toast.Refusal {
			st = pal.NeedsYou
			prefix = "⚠ "
		}
		if strings.HasPrefix(v.Toast.Text, "✓") || strings.HasPrefix(v.Toast.Text, "⚠") {
			prefix = ""
		}
		left = line(seg{prefix + v.Toast.Text, st})
	case v.DemoPaused:
		left = line(seg{"⏸ demo paused", pal.NeedsYou})
	default:
		// Two forms, because the full one plus the right-hand mode label does not
		// fit 44 columns and a truncated key hint ("t to…") is worse than a
		// shorter list of keys. The narrow form keeps the three keys the §3.1
		// diagram leads with plus the remaining toggle; ␣/f/o/y stay in `?`.
		// While something is blocked on the reader, the footer stops listing
		// keys and names the one that gets them to where it can be answered.
		hint := "j/k move · ⏎ open · a returned · ? keys"
		if width < 56 {
			hint = "j/k · ⏎ open · ? keys"
		}
		if len(w.Asks) > 0 {
			hint = "⇥ jump to the session pane to answer"
			if width < 56 {
				hint = "⇥ answer in the session pane"
			}
		}
		left = line(seg{hint, pal.Deep})
	}

	var right []seg
	switch {
	case w.Session.State == state.SessionCompacting:
		right = line(seg{"⠿ compacting context…", pal.Settled})
	case !w.Connected:
		wait := 3 - int(now.Sub(v.DisconnectedAt)/time.Second)%3
		right = line(seg{fmt.Sprintf("retrying in %ds…", wait), pal.Settled})
	case scrollInfo != "":
		right = line(seg{scrollInfo, pal.Settled})
	case v.Follow:
		right = line(seg{"⟳ following", pal.Live})
	case v.Digest != nil:
		right = line(seg{"▌ changed while away", pal.Settled})
	case len(w.Quiet) > 0:
		// §1.5, off the tree's head. The pane may never claim "no subagents"
		// while the machine holds a hundred of them, but a census does not get
		// to sit above the work either — it is chrome, and this is where the
		// chrome lives. It outranks the column hint because the hint is a
		// reminder of a key that is also in `?`, and this is a fact about the
		// session that is stated nowhere else on this screen.
		text := fmt.Sprintf("%d quiet · s lists", len(w.Quiet))
		if width < 56 {
			text = fmt.Sprintf("%d quiet", len(w.Quiet))
		}
		right = line(seg{text, pal.Deep})
	case v.RightCol == "rate":
		right = line(seg{"token rate →", pal.Deep})
	default:
		right = line(seg{copySinceHint, pal.Deep})
	}
	return composeLR(left, right, width)
}

// renderIdleFrame is the §3.8 screen: header, body, bottom watching line.
func renderIdleFrame(w state.World, v *UIState, width, rows int, now time.Time, pal Palette) *frame {
	f := &frame{}
	left := line(seg{"AGENTS", pal.Primary})
	if id := headerSessionID(w, v); id != "" {
		left = append(left, seg{" · " + id, pal.Settled})
	}
	right := line(seg{"idle", pal.Settled})
	if !w.Connected {
		right = line(seg{copyDisconnected, pal.NeedsYou})
	}
	f.add("", composeLR(left, right, width))
	f.add("", rule(width, pal))
	body, bodyIDs := renderIdle(w, v, width, now, pal)
	for i, ln := range body {
		f.add(bodyIDs[i], ln)
	}

	pad := rows - len(f.lines) - 2
	for i := 0; i < pad; i++ {
		f.add("", nil)
	}
	f.add("", rule(width, pal))

	var bottomLeft []seg
	switch {
	case v.Toast.Text != "" && now.Sub(v.Toast.At) < toastFor:
		st := pal.Live
		prefix := "✓ "
		if v.Toast.Refusal {
			st = pal.NeedsYou
			prefix = "⚠ "
		}
		if strings.HasPrefix(v.Toast.Text, "✓") || strings.HasPrefix(v.Toast.Text, "⚠") {
			prefix = ""
		}
		bottomLeft = line(seg{prefix + v.Toast.Text, st})
	case v.DemoPaused:
		bottomLeft = line(seg{"⏸ demo paused", pal.NeedsYou})
	case w.Session.State == state.SessionEnded:
		bottomLeft = line(seg{"session ended", pal.Settled})
	case v.pinnedWaiting():
		// The pane was opened for a specific session that has not reached
		// the registry yet. Name it instead of attaching to a neighbour: a
		// monitor watching the wrong session is worse than one watching
		// none (§3.8a deviation, see UIState.sessionPicker).
		bottomLeft = line(
			seg{spinner(now), pal.Settled},
			seg{" " + copyWaitingPinned + shortSessionID(v.PinnedSession), pal.Settled},
		)
	default:
		bottomLeft = line(
			seg{spinner(now), pal.Settled},
			seg{" " + copyWatching, pal.Settled},
		)
	}
	// The right edge used to advertise [ ] browse runs. Earlier runs are on
	// disk and `agentpane runs` prints them; the pane itself is done with them.
	f.add("", composeLR(bottomLeft, line(seg{"? keys", pal.Deep}), width))
	return f
}

// renderCondensed is the §3.9 <12-rows mode: root + alert band + agent
// one-liners + "n more agents ↓".
func renderCondensed(w state.World, v *UIState, width, rows int, now time.Time, pal Palette) *frame {
	f := &frame{}
	entries := bandEntries(w, v, now)
	f.add("", headerLine(w, v, width, now, pal, bandAlertCount(entries)))
	if len(entries) > 0 {
		e := entries[0]
		label := line(
			seg{"⚑ " + e.slug, pal.NeedsYou},
			seg{"  " + e.kind, pal.Primary},
		)
		if e.dupes > 1 {
			label = append(label, seg{fmt.Sprintf(" ×%d", e.dupes), pal.NeedsYou})
		}
		f.add(selBand, composeLR(label, line(seg{formatSince(e.waited), pal.NeedsYou}), width))
	}
	f.addAll(event.MainAgentID, mainBlock(w, v, nil, width, now, pal)[:1])
	// §1.5 again: a short pane still may not pretend the suppressed agents do
	// not exist. The count costs one line and is selectable; it does not expand
	// here, because there is no room to expand into.
	if n := len(w.Quiet); n > 0 {
		q := truncSegs(line(seg{quietCountText(n), pal.Settled}), width)
		if v.SelID == selQuiet {
			q = reverseLine(q)
		}
		f.add(selQuiet, q)
	}
	// A short pane hides the returned rows too, and pays the same one line to
	// say so — it is the only route back to them here.
	if ln := finishedLine(w, v, width, pal); ln != nil {
		f.add(selFinished, ln)
	}

	agents := v.orderedAgents(w)
	avail := rows - len(f.lines) - 1
	if avail < 1 {
		avail = 1
	}
	top := 0
	if sel := agentIndex(agents, v.SelID); sel >= avail {
		top = sel - avail + 1
	}
	shown := 0
	for i := top; i < len(agents) && shown < avail; i++ {
		f.add(agents[i].ID, oneLiner(agents[i], v, width, now, pal))
		shown++
	}
	if rest := len(agents) - top - shown; rest > 0 {
		f.add("", line(seg{fmt.Sprintf("%d more agents ↓", rest), pal.Settled}))
	}
	return f
}

// renderTiny is the §3.9 <40-cols mode: dot + name + since only.
func renderTiny(w state.World, v *UIState, cols, rows int, now time.Time, pal Palette) *frame {
	f := &frame{}
	entries := 0
	for _, a := range w.Agents {
		if needsYou(a) {
			entries++
		}
	}
	right := []seg(nil)
	if entries > 0 {
		right = line(seg{fmt.Sprintf("⚑%d", entries), pal.NeedsYou})
	}
	// Under 40 columns there is no room for the §1.5 notice as a row, so the
	// count rides on the header instead: "AGENTS 5 +40". It is the one place
	// the suppressed agents can be stated here, and stating them somewhere is
	// not optional — but it is not a selectable row, so selectionIDs must leave
	// selQuiet out at this width (quietLineDrawn).
	head := line(seg{fmt.Sprintf("AGENTS %d", len(w.Agents)), pal.Primary})
	if n := len(w.Quiet); n > 0 {
		head = append(head, seg{fmt.Sprintf(" +%d", n), pal.Settled})
	}
	// Same bargain for the returned rows this width filters out: no room for
	// the line, so the count rides on the header rather than going unsaid.
	if n := v.finishedHidden(w); n > 0 {
		head = append(head, seg{fmt.Sprintf(" ✓%d", n), pal.Settled})
	}
	f.add("", composeLR(head, right, cols))
	mainLn := composeLR(
		line(seg{"◍", pal.Live}, seg{" main", pal.Primary}),
		line(seg{formatSince(now.Sub(w.Main.LastEventAt)), pal.Deep}), cols)
	if v.SelID == event.MainAgentID {
		mainLn = reverseLine(mainLn)
	}
	f.add(event.MainAgentID, mainLn)
	agents := v.orderedAgents(w)
	avail := rows - 3
	if avail < 1 {
		avail = 1
	}
	top := 0
	if sel := agentIndex(agents, v.SelID); sel >= avail {
		top = sel - avail + 1
	}
	for i := top; i < len(agents) && i-top < avail; i++ {
		f.add(agents[i].ID, oneLiner(agents[i], v, cols, now, pal))
	}
	f.add("", line(seg{copyWiden, pal.Deep}))
	return f
}

// oneLiner is the degraded single-column row: dot + slug + since.
func oneLiner(a state.Agent, v *UIState, width int, now time.Time, pal Palette) []seg {
	dot, dotStyle, nameStyle := rowGlyph(a, pal)
	left := line(seg{dot, dotStyle}, seg{" " + v.slugFor(a), nameStyle})
	var right []seg
	if !a.Teammate && a.Status != state.StatusQueued && !a.Decayed {
		st := pal.Deep
		if needsYou(a) {
			st = pal.NeedsYou
		}
		right = line(seg{formatSince(now.Sub(a.LastEventAt)), st})
	}
	ln := composeLR(left, right, width)
	if a.ID == v.SelID {
		ln = reverseLine(ln)
	}
	return ln
}

func agentIndex(agents []state.Agent, id string) int {
	for i, a := range agents {
		if a.ID == id {
			return i
		}
	}
	return -1
}

// finishMeta flattens a frame into the final string plus hit-test metadata,
// clipped/padded to exactly rows lines.
func finishMeta(f *frame, width, rows int) (string, *frameMeta) {
	return finish(f.lines, f.ids, width, rows)
}

func finish(lines [][]seg, ids []string, width, rows int) (string, *frameMeta) {
	meta := &frameMeta{lineIDs: make([]string, rows)}
	out := make([]string, rows)
	for i := 0; i < rows; i++ {
		if i < len(lines) {
			out[i] = renderSegs(truncSegs(lines[i], width))
			if ids != nil && i < len(ids) {
				meta.lineIDs[i] = ids[i]
			}
		}
	}
	return strings.Join(out, "\n"), meta
}
