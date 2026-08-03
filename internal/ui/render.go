package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/state"
)

// frameMeta maps rendered lines back to agent ids for mouse hit-testing
// (§5.1: click selects, double-click inspects).
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

// narrationPlan splits the pane's leftover rows between the tree and the region
// below it, and returns what to draw there plus the tree rendered in the rest.
//
// **The rule at small geometries: the tree wins on breadth, the alert wins over
// prose.**
//
// Prose is reserved at its fixed height only while the tree still draws every
// agent's row AND every agent's activity line. Both signals come from renderTree
// itself, so the rule cannot drift from what the tree actually did: scrollInfo is
// set exactly when a row had to be hidden, and breadth is set exactly when the
// breadth pass finished. The ladder is then: try the requested mode; while either
// signal says the tree lost something, drop one region (commentary first, then
// the standup) and try again. Regions are dropped WHOLE and never shrunk, because
// a narration region whose height depends on the pane is precisely what §13.4
// forbids.
//
// What narration IS allowed to cost is tool-call history, and that is not a
// concession — it is the mock's own behaviour, whose call budget shrinks 14 → 8
// → 4 as the voice grows. History is discretionary (§3.17 already rations it
// 4→2→0 under pressure) and it is the one part of a row whose absence costs you
// nothing you cannot get back by selecting the row. A missing agent row, or a
// row that will not say what its agent is doing, is information you cannot
// recover at all, and no sentence is worth it.
//
// **But the alert is not prose and is not discretionary.** The standup carries
// the band now (§3.7 via band.go), so withholding the region entirely would
// withhold the ask — and §3.20 puts the band at intrusion level 1, "always". So
// when the prose is dropped and there is still an alert, the region keeps the
// band's own content alone (renderAlertOnly): the same rows the old band region
// spent, in the same slot below the tree, with no sentences attached. That costs
// the tree about what the band used to cost it and never more.
//
// The absence of the prose is stated, not silent: the `n` toast says "no room"
// (narrationFits) and the footer keeps advertising the key, so the mode returns
// the moment the pane grows.
//
// Everything below 40 columns (renderTiny), under 12 rows (renderCondensed) and
// the whole idle screen never reach this function: those geometries exist to
// carry one alert, and prose would displace it.
//
// minTreeRows is the floor the region must leave the tree: main's own two rows
// plus one agent. Below that the pane is not showing a tree at all, so the
// voice degrades instead (§13.2: the tree wins when there is not room for both).
const minTreeRows = 3

// narrPlan is what to draw below the tree, and the tree that fits above it.
//
// mode is the voice actually drawn. alertRows > 0 means the prose was dropped and
// the band is being drawn on its own in that many rows — which only ever happens
// at mode == VoiceOff, so the two can never both be on screen.
type narrPlan struct {
	mode      VoiceMode
	alertRows int
	tree      treeResult
}

func narrationPlan(w state.World, v *UIState, width, leftover int, now time.Time, pal Palette) narrPlan {
	entries := bandEntries(w, v, now)
	// Every attempt starts from the same viewport. renderTree writes
	// UIState.ScrollTop as a side effect of windowing (scrollWindow), so a
	// REJECTED candidate — which is windowed by definition — would otherwise
	// leave its scroll position behind for the candidate that is actually drawn.
	top := v.ScrollTop
	attempt := func(reserve int) treeResult {
		v.ScrollTop = top
		avail := leftover - reserve
		if avail < minTreeRows {
			avail = minTreeRows
		}
		return renderTree(w, v, width, avail, now, pal)
	}

	for mode := v.voice(); mode != VoiceOff; mode = mode.degraded() {
		h := narrationHeight(mode)
		tree := attempt(h)
		// The tree's own signals are not sufficient on their own. `avail` is
		// clamped to a floor of minTreeRows, so a tree with few rows reports "not
		// scrolling, full breadth" however little room it was given — and a
		// main-only run (every session before its first Task) has almost no
		// rows. Accepting on those signals alone let a 20-row narration onto an
		// 8-row leftover: the pad went negative and the footer, carrying the
		// toasts and key hints, was clipped off the frame.
		//
		// The guard is only that the region must leave the tree its floor. It is
		// deliberately not "region + every tree row must fit": the tree is
		// allowed to window inside its share, and requiring the whole tree
		// would switch the voice off on any pane with a few agents.
		if h <= leftover-minTreeRows && tree.scrollInfo == "" && tree.breadth {
			return narrPlan{mode: mode, tree: tree}
		}
	}

	// No prose. If there is a band, it still gets its rows — see the header.
	if want := alertOnlyRows(entries, v.Digest, width, pal); want > 0 {
		if room := leftover - minTreeRows; want > room {
			want = room
		}
		// alertOnlyMin is the chip plus the oldest entry. Below that there is
		// genuinely nowhere to put the band and the header's `⚑ n NEEDS YOU` is
		// the only surface left — which is also the geometry renderCondensed
		// exists for, one row further down.
		if want >= alertOnlyMin {
			return narrPlan{mode: VoiceOff, alertRows: want, tree: attempt(want)}
		}
	}
	return narrPlan{mode: VoiceOff, tree: attempt(0)}
}

// narrationFits reports whether the requested voice mode is the one being drawn.
// The `n` toast uses it to say "no room" instead of claiming a mode that is not
// on screen (§1.5: degrade honestly, never silently).
func narrationFits(w state.World, v *UIState, cols, rows int, now time.Time, pal Palette) bool {
	if v.voice() == VoiceOff {
		return true
	}
	top := v.ScrollTop
	plan := narrationPlan(w, v, frameWidth(v, cols), narrationLeftover(w, v, cols, rows, now, pal), now, pal)
	v.ScrollTop = top
	return plan.mode == v.voice()
}

// narrationLeftover recomputes the rows the tree and the region below it share.
// It mirrors renderActive's own arithmetic; keeping it in one small function is
// what stops the two from drifting. There is no band term any more — the band is
// inside the region, which is the whole point of §13.2's three-region table.
func narrationLeftover(w state.World, v *UIState, cols, rows int, now time.Time, pal Palette) int {
	width := frameWidth(v, cols)
	fixed := 2 + 2
	if len(w.Contentions) > 0 {
		fixed += len(renderContention(w, v, width, pal))
	}
	if v.InspectorOpen {
		fixed += len(renderInspector(w, v, width, now, pal))
	}
	return rows - fixed
}

// frameWidth is the rendered width for a pane of cols columns: the §3.1 wide-64
// or narrow-44 budget, never the raw terminal width.
func frameWidth(v *UIState, cols int) int {
	width := cols
	if v.WidthMode == "narrow" && width > 44 {
		return 44
	}
	if width > 64 {
		return 64
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
// Three regions, in §13.2's order: header, tree, then the standup (which IS the
// alert band) and the commentary. Nothing fixed-height sits above the tree, so
// the tree's first row is always y=2 — that is the §13.4 invariant, held by
// construction rather than by arithmetic.
func renderActive(w state.World, v *UIState, width, rows int, now time.Time, pal Palette) *frame {
	f := &frame{}
	entries := bandEntries(w, v, now)

	f.add("", headerLine(w, v, width, now, pal, len(entries)))
	f.add("", rule(width, pal))

	var inspLines [][]seg
	if v.InspectorOpen {
		inspLines = renderInspector(w, v, width, now, pal)
	}

	var contLines [][]seg
	if len(w.Contentions) > 0 {
		contLines = renderContention(w, v, width, pal)
	}

	// header(2) + contention + inspector + footer rule + footer
	fixed := 2 + len(contLines) + len(inspLines) + 2
	// The tree and the region below it share what is left. narrationPlan decides
	// the split, and the tree is rendered inside it — see the function for the
	// rule.
	plan := narrationPlan(w, v, width, rows-fixed, now, pal)
	var narrLines [][]seg
	var narrIDs []string
	if plan.alertRows > 0 {
		narrLines, narrIDs = renderAlertOnly(entries, v.Digest, v, width, plan.alertRows, pal)
	} else {
		narrLines, narrIDs = renderNarration(w, v, plan.mode, width, now, pal)
	}
	tree := plan.tree

	// Last-resort clips, in priority order. renderTree cannot go below main's own
	// block plus one agent's, and renderInspector has a fixed height of its own, so
	// on a very short pane either can hand back more lines than the budget it was
	// given — and the excess used to push the footer, which carries the toasts and
	// every key hint, straight off the bottom of the frame.
	//
	// minTreeRows is the floor the region leaves the tree; this is the matching
	// ceiling. The tree gives up rows first, then the inspector; the footer never
	// does, because a pane whose keys have scrolled away is a pane you cannot get
	// out of.
	treeLines, treeIDs := tree.lines, tree.ids
	// chrome is the header and the footer with their rules: the two rows at each
	// end that every active frame owns unconditionally.
	const chrome = 4
	rest := rows - chrome - len(narrLines) - len(contLines)
	if rest < 0 {
		rest = 0
	}
	if len(treeLines) > rest {
		treeLines, treeIDs = treeLines[:rest], treeIDs[:rest]
	}
	if rest -= len(treeLines); len(inspLines) > rest {
		inspLines = inspLines[:rest]
	}
	for i, ln := range treeLines {
		f.add(treeIDs[i], ln)
	}
	f.addAll("", contLines)

	// Pad so the narration, the inspector and the footer sit at the bottom.
	pad := rows - len(f.lines) - len(narrLines) - len(inspLines) - 2
	for i := 0; i < pad; i++ {
		f.add("", nil)
	}
	// Only the band's own lines inside the region carry a row id (selBand); prose
	// carries none, because §5.1 lists no such target and a click on a sentence
	// must not move the tree's selection.
	for i, ln := range narrLines {
		id := ""
		if i < len(narrIDs) {
			id = narrIDs[i]
		}
		f.add(id, ln)
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
	left := line(seg{fmt.Sprintf("AGENTS %d", len(w.Agents)), pal.Primary})
	if id := headerSessionID(w, v); id != "" {
		left = append(left, seg{" · " + id, pal.Settled})
	}
	var right []seg
	switch {
	case !w.Connected:
		right = line(seg{copyDisconnected, pal.NeedsYou})
	case alerts > 0:
		right = line(seg{fmt.Sprintf("⚑ %d NEEDS YOU", alerts), pal.NeedsYou})
	case w.Run != nil:
		t := formatClock(now.Sub(w.Run.StartedAt))
		if tok := formatTokens(w.Run.Tokens); tok != "" {
			t += " · " + tok + " tok"
		}
		right = line(seg{t, pal.Settled})
	}
	return composeLR(left, right, width)
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
		// `n voice` is advertised even when narrationPlan is withholding the
		// region: the key is what makes the absence recoverable, and hiding the
		// hint would make a pane with no room look like a pane with no feature.
		//
		// Two forms, because the full one plus the right-hand mode label does not
		// fit 44 columns and a truncated key hint ("t to…") is worse than a
		// shorter list of keys. The narrow form keeps the three keys the §3.1
		// diagram leads with plus the two toggles; ␣/f/o/y stay in `?`.
		hint := "j/k ␣ ⏎ f o y · n voice · t tokens"
		if width < 56 {
			hint = "j/k ⏎ · n voice · t tokens"
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
	case v.RightCol == "rate":
		right = line(seg{"token rate →", pal.Deep})
	default:
		right = line(seg{copySinceHint, pal.Deep})
	}
	return composeLR(left, right, width)
}

// renderContention renders §2.7: newest contention plus "+n more".
func renderContention(w state.World, v *UIState, width int, pal Palette) [][]seg {
	if len(w.Contentions) == 0 {
		return nil
	}
	c := w.Contentions[0]
	var out [][]seg
	out = append(out, rule(width, pal))
	out = append(out, truncSegs(line(seg{fmt.Sprintf("⚠ %d agents editing %s",
		len(c.AgentIDs), shortPath(c.Path)), pal.NeedsYou}), width))
	names := make([]string, 0, len(c.AgentIDs))
	for i, id := range c.AgentIDs {
		name := ""
		if i < len(c.AgentNames) {
			name = c.AgentNames[i]
		}
		names = append(names, v.slugOf(w, id, name))
	}
	second := line(seg{"  " + strings.Join(names, " + "), pal.Settled})
	if len(w.Contentions) > 1 {
		second = composeLR(second, line(seg{fmt.Sprintf("+%d more", len(w.Contentions)-1), pal.Deep}), width)
	}
	out = append(out, truncSegs(second, width))
	return out
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
		bottomLeft = line(seg{"session ended · [ ] browse runs", pal.Settled})
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
	f.add("", composeLR(bottomLeft, line(seg{"[ ] browse runs", pal.Deep}), width))
	return f
}

// renderCondensed is the §3.9 <12-rows mode: root + alert band + agent
// one-liners + "n more agents ↓".
func renderCondensed(w state.World, v *UIState, width, rows int, now time.Time, pal Palette) *frame {
	f := &frame{}
	entries := bandEntries(w, v, now)
	f.add("", headerLine(w, v, width, now, pal, len(entries)))
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
