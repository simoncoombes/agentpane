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

// §3.3 puts up to max_calls_shown tool calls under an expanded agent. Breadth
// (one activity line each) comes first, but rows left over after that must
// become call history rather than whitespace.
func TestSpareRoomAlsoShowsToolCalls(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	render := func(rows int) string {
		frame, _ := renderFrame(w, v, 64, rows, demoNow(), NewPalette(2, false))
		return stripANSI(frame)
	}

	// callIndent ("  │ ") prefixes every rendered tool call.
	countCalls := func(s string) int { return strings.Count(s, callIndent) }

	tall, short := countCalls(render(60)), countCalls(render(18))
	if tall <= short {
		t.Errorf("a tall pane must show more tool calls: tall=%d short=%d", tall, short)
	}
	if tall < 4 {
		t.Errorf("expected several tool-call lines in a 60-row pane, got %d:\n%s", tall, render(60))
	}

	// Depth must never cost breadth: every agent that had an activity line in
	// the short pane still has one in the tall pane.
	if a, b := strings.Count(render(60), "⤷"), strings.Count(render(18), "⤷"); a < b {
		t.Errorf("depth pass dropped activity lines: tall=%d short=%d", a, b)
	}

	// And it still may not overflow at any height.
	for _, rows := range []int{12, 18, 24, 40, 60} {
		if n := len(strings.Split(strings.TrimRight(render(rows), "\n"), "\n")); n > rows {
			t.Errorf("rows=%d produced %d lines", rows, n)
		}
	}
}
