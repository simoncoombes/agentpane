package ui

import (
	"strings"
	"testing"

	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/state"
)

// A tall pane must not sit two-thirds empty while every agent hides what it
// is doing: spare rows become activity lines, breadth first.
func TestSpareRoomShowsWhatAgentsAreDoing(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	render := func(rows int) string {
		frame, _ := renderFrame(w, v, 64, rows, demoNow(), NewPalette(2, false))
		return stripANSI(frame)
	}

	tall := render(60)
	short := render(16)

	countArrows := func(s string) int { return strings.Count(s, nowMark) }
	tallN, shortN := countArrows(tall), countArrows(short)
	if tallN <= shortN {
		t.Errorf("a tall pane must reveal more activity lines: tall=%d short=%d", tallN, shortN)
	}
	if tallN < 2 {
		t.Errorf("expected several activity lines in a 60-row pane, got %d:\n%s", tallN, tall)
	}

	// Growth may never push the tree into scrolling, at any height.
	for _, rows := range []int{12, 16, 20, 30, 45, 60} {
		frame := render(rows)
		if n := len(strings.Split(strings.TrimRight(frame, "\n"), "\n")); n > rows {
			t.Errorf("rows=%d produced %d lines — auto-expansion overflowed", rows, n)
		}
	}
}

// A row's first job is one line of what the agent is DOING, and under
// pressure that is all it is: history is one ⏎ away in the inspector. But rows
// that would otherwise be padding go back into the strip, so a quiet pane
// watching one agent says more than a name and a timer.
func TestHistoryGrowsIntoSpareRooms(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	render := func(rows int) string {
		frame, _ := renderFrame(w, v, 64, rows, demoNow(), NewPalette(2, false))
		return stripANSI(frame)
	}
	countCalls := func(s string) int { return strings.Count(s, contMid+histIndent) }

	// Tight: every row is spoken for, so no agent carries history.
	if n := countCalls(render(18)); n != 0 {
		t.Errorf("rows=18: %d history lines on a tree with no room for them", n)
	}
	// Roomy: the padding becomes history instead.
	if n := countCalls(render(60)); n == 0 {
		t.Errorf("rows=60: no history lines in a pane with rows to spare:\n%s", render(60))
	}

	// Breadth still beats depth: a tall pane shows more agents doing things,
	// and never fewer than a short one.
	if a, b := strings.Count(render(60), nowMark), strings.Count(render(18), nowMark); a < b {
		t.Errorf("a tall pane shows fewer agents doing things: tall=%d short=%d", a, b)
	}

	// Pinning one row brings its history back even when there is no room to
	// grow, and only its own.
	for _, a := range w.Agents {
		if expandable(a) {
			v.PinSeq++
			v.Pins[a.ID] = v.PinSeq
			break
		}
	}
	if n := countCalls(render(60)); n == 0 {
		t.Errorf("a pinned row shows no history:\n%s", render(60))
	}

	// And it still may not overflow at any height.
	for _, rows := range []int{12, 18, 24, 40, 60} {
		if n := len(strings.Split(strings.TrimRight(render(rows), "\n"), "\n")); n > rows {
			t.Errorf("rows=%d produced %d lines", rows, n)
		}
	}
}

// One agent working alone in a tall pane is the common case, and the case the
// old "spare rows are spare" rule served worst: three lines of tree and thirty
// of blank. It must now spend those rows on what that agent has been doing.
func TestLoneAgentFillsATallPane(t *testing.T) {
	now := demoNow()
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	v.SelID = event.MainAgentID

	// Keep the busiest agent and drop the rest.
	var lone state.Agent
	for _, a := range w.Agents {
		if expandable(a) && len(a.CallHistory) > len(lone.CallHistory) {
			lone = a
		}
	}
	if lone.ID == "" {
		t.Fatal("no expandable agent with call history in the demo world")
	}
	w.Agents = []state.Agent{lone}

	frame, meta := renderFrame(w, v, 64, 40, now, NewPalette(2, false))
	lines := strings.Split(stripANSI(frame), "\n")
	var block []string
	for i, id := range meta.lineIDs {
		if id == lone.ID {
			block = append(block, lines[i])
		}
	}
	// branch + row head + at least one history line + the activity line.
	if len(block) < 4 {
		t.Errorf("a lone agent in a 40-row pane got %d lines:\n%s",
			len(block), strings.Join(block, "\n"))
	}
	if len(block) > 0 && !strings.Contains(block[len(block)-1], nowMark+" ") {
		t.Errorf("the block does not end on its activity line:\n%s",
			strings.Join(block, "\n"))
	}
}
