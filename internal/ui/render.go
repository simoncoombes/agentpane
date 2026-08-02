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
	width := cols
	if v.WidthMode == "narrow" && width > 44 {
		width = 44
	} else if width > 64 {
		width = 64
	}
	if idlePhase(w) {
		return finishMeta(renderIdleFrame(w, v, width, rows, now, pal), width, rows)
	}
	if rows < 12 {
		return finishMeta(renderCondensed(w, v, width, rows, now, pal), width, rows)
	}
	return finishMeta(renderActive(w, v, width, rows, now, pal), width, rows)
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
func renderActive(w state.World, v *UIState, width, rows int, now time.Time, pal Palette) *frame {
	f := &frame{}
	entries := bandEntries(w, v, now)
	bandPresent := len(entries) > 0 || v.Digest != nil

	f.add("", headerLine(w, v, width, now, pal, len(entries)))
	f.add("", rule(width, pal))

	var bandLines [][]seg
	if bandPresent {
		bandLines = renderBand(entries, v, width, v.SelID == selBand, pal)
		// A full band on a short pane must not push the tree and footer off
		// screen: clip it, keeping the chip and the oldest entry (§3.7.6).
		if maxBand := rows - 12; len(bandLines) > maxBand && maxBand >= 3 {
			bandLines = bandLines[:maxBand]
		}
	}

	var inspLines [][]seg
	if v.InspectorOpen {
		inspLines = renderInspector(w, v, width, now, pal)
	}

	var contLines [][]seg
	if len(w.Contentions) > 0 {
		contLines = renderContention(w, v, width, pal)
	}

	// header(2) + band(+rule) + contention + inspector + footer rule + footer
	fixed := 2 + len(contLines) + len(inspLines) + 2
	if len(bandLines) > 0 {
		fixed += len(bandLines) + 1
	}
	treeAvail := rows - fixed
	if treeAvail < 3 {
		treeAvail = 3
	}
	tree := renderTree(w, v, width, treeAvail, now, pal)

	for _, ln := range bandLines {
		f.add(selBand, ln)
	}
	if len(bandLines) > 0 {
		f.add("", rule(width, pal))
	}
	for i, ln := range tree.lines {
		f.add(tree.ids[i], ln)
	}
	f.addAll("", contLines)

	// Pad so the inspector and footer sit at the bottom.
	pad := rows - len(f.lines) - len(inspLines) - 2
	for i := 0; i < pad; i++ {
		f.add("", nil)
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
		left = line(seg{"j/k ␣ ⏎ f o y · t tokens", pal.Deep})
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
