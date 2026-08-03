package ui

import (
	"strings"
	"time"

	"github.com/simoncoombes/agentpane/internal/state"
)

// PART 13 Q8 — the suppressed-agents inspector.
//
// "⏎ on it opens the inspector with the full list, per-agent spawn time and last
// event, because the one time you care about phantoms is when you suspect one
// isn't a phantom."
//
// That sentence sets the whole design brief: this is not a nicer version of the
// tree's `+n` count, it is the screen where someone decides whether a suppressed
// agent is real. The evidence that settles it is per-agent and comparative —
// DATA-SOURCES records 40 phantoms that started and stopped in the same instant
// alongside four REAL agents that were suppressed for ~90 seconds while alive —
// so spawn and last-event sit side by side and the reader subtracts. An agent
// whose two times are equal announced itself and returned; one whose last event
// is a minute after its spawn was doing something the row rule never saw, and
// that is the case this view exists to expose.
//
// The id is shown as the platform's own id, not the tree's placeholder slug: the
// next step after suspecting a phantom is grepping a transcript or a hook log
// for it, and a slug is not in either. Truncation is therefore marked, never
// silent (truncSegs' …), and the columns never drop — the four fields ARE the
// view.
const (
	// quietTimeFormat is wall-clock, deliberately absolute. Every other clock
	// in the pane is relative because the question is "how long has this been
	// going", but here the question is "which line of the transcript is this",
	// and a relative age cannot be matched against a log.
	quietTimeFormat = "15:04:05"
	quietTimeWidth  = 8
	// quietLastHeader is wider than a clock, so the last column is sized by its
	// header rather than its values: abbreviating it to "last" would make the
	// one comparison this view exists for ambiguous.
	quietLastHeader = "last event"

	// quietColGap is the space between columns; quietIndent aligns rows under
	// the header the way the tree indents its expansion.
	quietColGap = 2
	quietIndent = 2

	quietIDMin   = 8
	quietTypeMin = 6

	copyQuietClose = "esc close view"
	copyQuietKeys  = "wheel scrolls"

	// copyQuietWhy is the honest definition, matching state.earnsRow's negation
	// and §13.1's amendment: "no recorded activity" means no tool call and no
	// tokens, not "nothing yet".
	copyQuietWhy = "announced · no name, no tool call, no tokens"

	// copyQuietNone is the degenerate case: the view was opened with nothing to
	// list. Reachable only if the machine drops a suppressed agent between the
	// frame that drew the line and the keypress that opened it.
	copyQuietNone = "nothing suppressed — every announced agent has a row"
)

// phantomRow is one suppressed agent as this view needs it: the four §13.3 Q8
// fields plus the status that decides whether it is still alive.
//
// It is a view type rather than state.QuietAgent because state.QuietAgent
// currently carries only ID, Type and Status — the two times do not exist in the
// snapshot yet (see quietRowsNeeds). Keeping the view's shape complete means the
// columns are already correct when the machine starts supplying them, and the
// absence renders as — in the meantime rather than as a plausible-looking zero
// time.
type phantomRow struct {
	ID          string
	Type        string
	Status      state.Status
	SpawnedAt   time.Time
	LastEventAt time.Time
}

// quietRows adapts the snapshot into view rows, in the machine's order (spawn
// order — state.Machine appends World.Quiet as it walks its agent map in agent
// order).
//
// What state.QuietAgent would need for the times to be real, for whoever owns
// internal/state next:
//
//	SpawnedAt   time.Time // agentState.spawnedAt, already tracked
//	LastEventAt time.Time // agentState.lastEventAt, already tracked
//
// Both values exist on state.agentState today and are copied into state.Agent
// for every agent that earns a row; the suppressed path simply does not carry
// them across. Until it does, this view prints — for both, which is the honest
// rendering of "the model knows and did not tell us" and is what
// TestQuietInspectorHonestTimes pins.
func quietRows(w state.World) []phantomRow {
	out := make([]phantomRow, 0, len(w.Quiet))
	for _, q := range w.Quiet {
		out = append(out, phantomRow{ID: q.ID, Type: q.Type, Status: q.Status})
	}
	return out
}

// quietCols is the column layout for a given width. The times are fixed; the id
// and type share what is left, 60/40, because an id is the field the reader
// takes elsewhere and the type is a category with short values.
func quietCols(width int) (idW, typeW int) {
	fixed := quietIndent + 3*quietColGap + quietTimeWidth + len(quietLastHeader)
	rest := width - fixed
	if rest < quietIDMin+quietTypeMin {
		rest = quietIDMin + quietTypeMin
	}
	idW = rest * 6 / 10
	if idW < quietIDMin {
		idW = quietIDMin
	}
	typeW = rest - idW
	if typeW < quietTypeMin {
		typeW = quietTypeMin
	}
	return idW, typeW
}

// quietCell pads or visibly cuts one column value.
func quietCell(s string, w int) string {
	if s == "" {
		s = "—"
	}
	s = flatten(s)
	if len([]rune(s)) > w {
		return truncString(s, w-1) + "…"
	}
	return s + strings.Repeat(" ", w-len([]rune(s)))
}

// quietIDCell cuts an id from the MIDDLE, not the end.
//
// Platform agent ids share long heads — DATA-SOURCES records a captured run
// whose phantom ids differed only in their last two characters — so a prefix
// truncation renders nine identical-looking rows and destroys the one thing this
// view is for. Eliding the middle keeps both ends, so the rows stay distinct and
// the tail is still greppable. The tail gets the odd column because the tail is
// where the entropy is.
func quietIDCell(s string, w int) string {
	if s == "" {
		s = "—"
	}
	s = flatten(s)
	r := []rune(s)
	if len(r) <= w {
		return s + strings.Repeat(" ", w-len(r))
	}
	if w <= 3 {
		return truncString(s, w)
	}
	tail := (w - 1 + 1) / 2
	head := w - 1 - tail
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

func quietTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format(quietTimeFormat)
}

// quietUnsettled counts suppressed agents that have not returned. A suppressed
// agent that is STILL RUNNING is the strongest single argument that it is not a
// phantom, so the count is stated even though it is not one of the four columns.
func quietUnsettled(rows []phantomRow) int {
	n := 0
	for _, r := range rows {
		switch r.Status {
		case state.StatusDone, state.StatusTeammate:
		default:
			n++
		}
	}
	return n
}

// quietWindow picks the visible slice, bottom-anchored: scroll is rows ABOVE the
// bottom, exactly the semantics renderInspector already uses for the log and the
// mouse wheel already drives (wheel-up = earlier). The newest-announced agents
// are the default view because a suppressed agent that is still alive is the one
// worth finding, and it is necessarily among the recent ones.
func quietWindow(total, budget, scroll int) (start, end int) {
	if budget < 1 {
		budget = 1
	}
	if scroll < 0 {
		scroll = 0
	}
	if scroll > total-budget {
		scroll = total - budget
	}
	if scroll < 0 {
		scroll = 0
	}
	end = total - scroll
	start = end - budget
	if start < 0 {
		start = 0
	}
	return start, end
}

// quietRange is the visible-window notice. It appears ONLY when the list does
// not fit, so a short list is not decorated with arithmetic, and it is what makes
// the truncation visible rather than implied.
func quietRange(start, end, total int) string {
	if total <= end-start {
		return itoa(total) + " announced"
	}
	return "showing " + itoa(start+1) + "–" + itoa(end) + " of " + itoa(total)
}

// renderQuietInspector is the PART 4 bottom split for the `+n agents with no
// recorded activity` line (selQuiet). Same 14-row budget and same top rule as
// renderInspector, so the two views cannot disagree about the pane's geometry.
func renderQuietInspector(rows []phantomRow, scroll, width int, pal Palette) [][]seg {
	var lines [][]seg
	lines = append(lines, rule(width, pal))

	if len(rows) == 0 {
		lines = append(lines, line(seg{"▣ suppressed agents", pal.Primary}))
		lines = append(lines, truncSegs(line(seg{copyQuietNone, pal.Settled}), width))
		lines = append(lines, composeLR(line(seg{copyQuietClose, pal.Deep}), nil, width))
		return lines
	}

	// rule + head + why + column header + footer.
	budget := inspectorMax - 5
	if budget < 1 {
		budget = 1
	}
	start, end := quietWindow(len(rows), budget, scroll)

	lines = append(lines, composeLR(
		line(seg{"▣ suppressed agents", pal.Primary}),
		line(seg{quietRange(start, end, len(rows)), pal.Settled}),
		width))

	var unsettled []seg
	if n := quietUnsettled(rows); n > 0 {
		// Bold, not just dim: an unsettled suppressed agent is the thing this
		// screen is for, and NeedsYou is the pane's only "look here" weight.
		unsettled = line(seg{itoa(n) + " not returned", pal.NeedsYou})
	}
	lines = append(lines, composeLR(line(seg{copyQuietWhy, pal.Settled}), unsettled, width))

	idW, typeW := quietCols(width)
	gap := strings.Repeat(" ", quietColGap)
	lines = append(lines, truncSegs(line(seg{
		strings.Repeat(" ", quietIndent) +
			quietCell("id", idW) + gap +
			quietCell("type", typeW) + gap +
			quietCell("spawn", quietTimeWidth) + gap +
			quietLastHeader, pal.Deep}), width))

	for _, r := range rows[start:end] {
		lines = append(lines, truncSegs(line(seg{
			strings.Repeat(" ", quietIndent) +
				quietIDCell(r.ID, idW) + gap +
				quietCell(r.Type, typeW) + gap +
				quietCell(quietTime(r.SpawnedAt), quietTimeWidth) + gap +
				quietTime(r.LastEventAt), pal.Primary}), width))
	}

	lines = append(lines, composeLR(
		line(seg{copyQuietClose, pal.Deep}),
		line(seg{copyQuietKeys, pal.Deep}),
		width))

	if len(lines) > inspectorMax {
		lines = lines[:inspectorMax]
	}
	return lines
}
