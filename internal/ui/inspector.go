package ui

import (
	"strings"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/state"
)

// inspectorMax is the PART 4 cap: a bottom split of at most 14 rows
// (including its top rule), never a pixel measure.
const inspectorMax = 14

// renderInspector renders the PART 4 bottom split for the selected agent.
// Returned lines include the separating top rule. Closing it is view-only —
// nothing here touches the agent.
func renderInspector(w state.World, v *UIState, width int, now time.Time, pal Palette) [][]seg {
	// The suppressed-agents line is not an agent: it has no description, no
	// stations and no log, and the question asked of it is a different question
	// (§13.3 Q8). It gets its own view rather than an agent view full of
	// dashes.
	if v.SelID == selQuiet {
		return renderQuietInspector(quietRows(w), v.InspectorScroll, width, pal)
	}

	var lines [][]seg
	lines = append(lines, rule(width, pal))

	slugName, status, elapsed, desc, typ := inspectorIdentity(w, v, now)

	statusStyle := pal.Settled
	switch status {
	case "ASK", "STUCK":
		statusStyle = pal.NeedsYou
	case "RUN":
		statusStyle = pal.Live
	}
	head := line(
		seg{"▣ ", statusStyle},
		seg{slugName, pal.Primary},
		seg{" " + status, statusStyle},
	)
	if v.Follow {
		head = append(head, seg{"  ⟳ FOLLOW", pal.Live})
	}
	lines = append(lines, composeLR(head, line(seg{elapsed, pal.Deep}), width))

	// Full untruncated description, wrapped, subagent_type right-aligned on
	// the first line (§3.16, PART 4 item 2).
	descLines := wrapText(desc, width-len([]rune(typ))-2)
	if len(descLines) == 0 {
		descLines = []string{""}
	}
	first := composeLR(line(seg{descLines[0], pal.Primary}), line(seg{typ, pal.Settled}), width)
	lines = append(lines, first)
	for _, dl := range descLines[1:] {
		lines = append(lines, truncSegs(line(seg{dl, pal.Primary}), width))
	}

	// §13.1(a): when the exact description arrived after the row's name had
	// already been published — in a notification or in the standup — the row
	// keeps the published name and the truth is recorded HERE and nowhere else.
	// This is the only place in the pane allowed to show it: printing it on the
	// row would perform the rename the freeze exists to prevent.
	if v.Slugs != nil && v.SelID != "" {
		if late := v.Slugs.LateName(v.SelID); late != "" {
			lines = append(lines, truncSegs(line(
				seg{"named late · ", pal.Deep}, seg{late, pal.Settled}), width))
		}
	}

	lines = append(lines, inspectorStationRow(w, v, width, pal))
	// §13.3 Q1: the activity history is here, on the row's own cross-agent
	// scale, and the row gave up nothing for it.
	if hist := inspectorHistoryRow(w, v, width, now, pal); hist != nil {
		lines = append(lines, hist)
	}

	// Fixed rows so far + cursor + footer bound the log window.
	a, isAgent := agentByID(w, v.SelID)
	live := true
	if isAgent {
		live = a.Status == state.StatusRun || a.Status == state.StatusAsk || a.Status == state.StatusStuck
	}
	fixed := len(lines) + 1 // + footer
	if live {
		fixed++ // + cursor line
	}
	logBudget := inspectorMax - fixed
	if logBudget < 1 {
		logBudget = 1
	}
	all := v.Logs.Lines(v.SelID)
	scroll := v.InspectorScroll
	if scroll < 0 {
		scroll = 0
	}
	if scroll > len(all)-logBudget {
		scroll = len(all) - logBudget
	}
	if scroll < 0 {
		scroll = 0
	}
	end := len(all) - scroll
	start := end - logBudget
	if start < 0 {
		start = 0
	}
	for _, ll := range all[start:end] {
		lines = append(lines, truncSegs(line(seg{ll.Text, logStyle(ll.Kind, pal)}), width))
	}

	if live {
		// Blinking block cursor on the 1s tick (PART 4 item 5).
		cursor := "▌"
		if now.Unix()%2 != 0 {
			cursor = " "
		}
		lines = append(lines, line(seg{cursor, pal.Live}))
	}

	// Footer: exact copy + the open file as an OSC 8 link (§5.4).
	openFile := ""
	if isAgent {
		openFile = a.OpenFile
	} else if v.SelID == event.MainAgentID {
		openFile = ""
	}
	var right []seg
	if openFile != "" {
		// PART 4 footer shows the full path ("o src/api/handlers.ts") — it
		// names a real file to open, so it is never src/-shortened.
		text := "o " + openFile
		if v.LinksOn && v.Emitter != nil {
			text = v.Emitter.Hyperlink("file://"+openFile, text)
		}
		right = line(seg{text, pal.Settled})
	} else {
		right = line(seg{"no open file", pal.Deep})
	}
	lines = append(lines, composeLR(line(seg{copyInspectorClose, pal.Deep}), right, width))

	if len(lines) > inspectorMax {
		lines = lines[:inspectorMax]
	}
	return lines
}

func inspectorIdentity(w state.World, v *UIState, now time.Time) (slugName, status, elapsed, desc, typ string) {
	if v.SelID == event.MainAgentID || v.SelID == selBand {
		meta := "elapsed —"
		if w.Run != nil {
			meta = "elapsed " + formatClock(now.Sub(w.Run.StartedAt))
		}
		if t := formatTokens(w.Main.Tokens); t != "" {
			meta += " · " + t
		}
		// Same description treatment as an agent row: the FULL untruncated
		// workstream text, wrapped — main's row only has room for a
		// budgeted label, so this is where the whole title lives.
		desc = mainLabel(w)
		if desc == "" {
			desc = "root agent"
		}
		return event.MainAgentID, "RUN", meta, desc, ""
	}
	a, ok := agentByID(w, v.SelID)
	if !ok {
		return v.SelID, "UNKNOWN", "elapsed —", "", ""
	}
	end := now
	if a.Status == state.StatusDone && !a.DoneAt.IsZero() {
		end = a.DoneAt
	}
	meta := "elapsed " + formatClock(end.Sub(a.SpawnedAt))
	if t := formatTokens(a.Tokens); t != "" {
		meta += " · " + t
	}
	if a.LinesAdd > 0 || a.LinesDel > 0 {
		meta += " · " + diffStat(a.LinesAdd, a.LinesDel)
	}
	return v.slugFor(a), strings.ToUpper(a.Status.String()), meta, a.Name, a.Type
}

// inspectorStationRow is PART 4 item 3, or the §3.15 teammate variant.
func inspectorStationRow(w state.World, v *UIState, width int, pal Palette) []seg {
	if v.SelID == event.MainAgentID || v.SelID == selBand {
		todo := w.Main.Todo
		if todo == "" {
			todo = w.Main.Activity
		}
		return truncSegs(line(seg{"todo · " + todo, pal.Deep}), width)
	}
	a, ok := agentByID(w, v.SelID)
	if !ok {
		return nil
	}
	if a.Teammate {
		return line(seg{"no station data — limited telemetry", pal.Settled})
	}
	done := a.Status == state.StatusDone
	out := line(
		seg{pips(a.Station, done), pal.Deep},
		seg{"  " + stationLabel(a.Station, done), pal.Deep},
	)
	// The split marker (§13.3 Q6), not the yes/no one: a check that exited
	// non-zero is the more useful of the two outcomes and the boolean threw it
	// away. Still labelled "(inferred)" here, because a command's description
	// naming a test is a claim about intent — the exit code sharpens the marker,
	// it does not turn it into a station.
	switch a.Verify {
	case state.VerifyOK:
		out = composeLR(out, line(seg{"✓ verified (inferred)", pal.Ok}), width)
	case state.VerifyFailed:
		out = composeLR(out, line(seg{"✖ verify failed (inferred)", pal.NeedsYou}), width)
	}
	return out
}

func logStyle(k lineKind, pal Palette) Style {
	switch k {
	case lineNeedsYou:
		return pal.NeedsYou
	case lineError:
		// The ONLY ANSI 1/9 in the pane: inspector log error text
		// (§3.10.2a).
		return pal.Err
	case lineOK:
		return pal.Ok
	case lineDim:
		return pal.Settled
	default:
		return pal.Primary
	}
}

func diffStat(add, del int) string {
	return "+" + itoa(add) + " −" + itoa(del)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// wrapText wraps on spaces to at most w columns; overlong words are cut.
func wrapText(s string, w int) []string {
	if w < 8 {
		w = 8
	}
	if s == "" {
		return nil
	}
	var out []string
	cur := ""
	for _, word := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = word
		case len([]rune(cur))+1+len([]rune(word)) <= w:
			cur += " " + word
		default:
			out = append(out, cur)
			cur = word
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	// Never let the wrap explode the 14-row budget: two lines max, the
	// inspector shows the rest via Y (yank) and the full description is in
	// the row itself (§3.16).
	if len(out) > 2 {
		out[1] = truncString(out[1], w-1) + "…"
		out = out[:2]
	}
	return out
}
