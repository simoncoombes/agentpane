package ui

import (
	"strings"
	"testing"
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

// A row is one line of what the agent is DOING. Its history is not on the tree
// at all — it is one ⏎ away in the inspector — unless the row is pinned, which
// is an explicit per-row choice.
func TestHistoryIsADrillInNotARow(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	render := func(rows int) string {
		frame, _ := renderFrame(w, v, 64, rows, demoNow(), NewPalette(2, false))
		return stripANSI(frame)
	}
	countCalls := func(s string) int { return strings.Count(s, contMid+histIndent) }

	for _, rows := range []int{18, 30, 60} {
		if n := countCalls(render(rows)); n != 0 {
			t.Errorf("rows=%d: %d history lines on the tree, want none", rows, n)
		}
	}
	// A tall pane spends its rows on AGENTS, not on history for a few of them.
	if a, b := strings.Count(render(60), nowMark), strings.Count(render(18), nowMark); a <= b {
		t.Errorf("a tall pane must show more agents doing things: tall=%d short=%d", a, b)
	}

	// Pinning one row brings its history back, and only its own.
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
