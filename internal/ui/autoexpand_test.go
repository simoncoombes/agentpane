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

	countArrows := func(s string) int { return strings.Count(s, "⤷") }
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
