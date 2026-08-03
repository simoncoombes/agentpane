package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/state"
)

func quietNow() time.Time { return time.Date(2026, 8, 3, 14, 2, 0, 0, time.UTC) }

// phantoms builds n suppressed agents. Every second one gets a last event well
// after its spawn — the shape of a REAL agent that the row rule suppressed,
// which is the case this view exists to expose.
func phantoms(n int, now time.Time) []phantomRow {
	out := make([]phantomRow, 0, n)
	for i := 0; i < n; i++ {
		spawn := now.Add(-time.Duration(n-i) * time.Second)
		last := spawn
		st := state.StatusDone
		if i%2 == 1 {
			last = spawn.Add(90 * time.Second)
			st = state.StatusRun
		}
		out = append(out, phantomRow{
			ID:          fmt.Sprintf("ada0620f224b32d6%02d", i),
			Type:        "general-purpose",
			Status:      st,
			SpawnedAt:   spawn,
			LastEventAt: last,
		})
	}
	return out
}

func quietFrame(rows []phantomRow, scroll, width int) []string {
	lines := renderQuietInspector(rows, scroll, width, NewPalette(2, true))
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		out = append(out, stripANSI(renderSegs(ln)))
	}
	return out
}

// The four §13.3 Q8 fields, per agent, on one line each.
func TestQuietInspectorShowsFourFieldsPerAgent(t *testing.T) {
	now := quietNow()
	rows := phantoms(4, now)
	frame := quietFrame(rows, 0, 64)
	joined := strings.Join(frame, "\n")

	for _, want := range []string{"id", "type", "spawn", "last event"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no %q column header:\n%s", want, joined)
		}
	}
	for _, r := range rows {
		var found string
		for _, ln := range frame {
			if strings.Contains(ln, r.ID) {
				found = ln
			}
		}
		if found == "" {
			t.Fatalf("agent %s has no line:\n%s", r.ID, joined)
		}
		if !strings.Contains(found, "general-p") {
			t.Errorf("line %q lost the type", found)
		}
		if !strings.Contains(found, r.SpawnedAt.Format(quietTimeFormat)) {
			t.Errorf("line %q lost the spawn time (%s)", found, r.SpawnedAt.Format(quietTimeFormat))
		}
		if !strings.Contains(found, r.LastEventAt.Format(quietTimeFormat)) {
			t.Errorf("line %q lost the last-event time (%s)", found, r.LastEventAt.Format(quietTimeFormat))
		}
	}
}

// The judgement this view exists for: an agent whose last event is long after
// its spawn is visibly different from one that returned instantly.
func TestQuietInspectorMakesTheJudgementPossible(t *testing.T) {
	now := quietNow()
	rows := []phantomRow{
		{ID: "phantom-aaaa", Type: "general-purpose", Status: state.StatusDone,
			SpawnedAt: now.Add(-30 * time.Second), LastEventAt: now.Add(-30 * time.Second)},
		{ID: "real-bbbb", Type: "explore", Status: state.StatusRun,
			SpawnedAt: now.Add(-95 * time.Second), LastEventAt: now.Add(-2 * time.Second)},
	}
	frame := strings.Join(quietFrame(rows, 0, 64), "\n")

	if !strings.Contains(frame, "1 not returned") {
		t.Errorf("the unsettled count is the strongest not-a-phantom signal and is missing:\n%s", frame)
	}
	for _, ln := range quietFrame(rows, 0, 64) {
		if strings.Contains(ln, "phantom-aaaa") {
			// Both times identical: announced and returned.
			if strings.Count(ln, now.Add(-30*time.Second).Format(quietTimeFormat)) != 2 {
				t.Errorf("phantom line %q should show the same instant twice", ln)
			}
		}
		if strings.Contains(ln, "real-bbbb") {
			if !strings.Contains(ln, now.Add(-95*time.Second).Format(quietTimeFormat)) ||
				!strings.Contains(ln, now.Add(-2*time.Second).Format(quietTimeFormat)) {
				t.Errorf("real line %q should show two different instants", ln)
			}
		}
	}
}

// Beyond a screenful the list windows and SAYS it windowed: a silent cut here
// would be the exact failure the count line was invented to avoid.
func TestQuietInspectorPaginatesVisibly(t *testing.T) {
	now := quietNow()
	rows := phantoms(40, now)
	frame := quietFrame(rows, 0, 64)

	if len(frame) > inspectorMax {
		t.Fatalf("frame = %d lines, cap is %d", len(frame), inspectorMax)
	}
	joined := strings.Join(frame, "\n")
	if !strings.Contains(joined, "of 40") {
		t.Errorf("no visible window notice:\n%s", joined)
	}
	if !strings.Contains(joined, "showing ") {
		t.Errorf("the window is not stated:\n%s", joined)
	}

	// Scrolling reaches the earlier agents, and the notice tracks it.
	budget := inspectorMax - 5
	deep := quietFrame(rows, 40-budget, 64)
	deepJoined := strings.Join(deep, "\n")
	if !strings.Contains(deepJoined, "showing 1–"+itoa(budget)) {
		t.Errorf("scrolled to the top, notice = wrong:\n%s", deepJoined)
	}
	if !strings.Contains(deepJoined, rows[0].ID[:12]) {
		t.Errorf("the oldest suppressed agent is unreachable:\n%s", deepJoined)
	}
	// Over-scroll clamps instead of emptying the list.
	clamped := strings.Join(quietFrame(rows, 9999, 64), "\n")
	if !strings.Contains(clamped, "showing 1–"+itoa(budget)) {
		t.Errorf("over-scroll did not clamp:\n%s", clamped)
	}

	// A list that fits is not decorated with arithmetic.
	short := strings.Join(quietFrame(phantoms(3, now), 0, 64), "\n")
	if strings.Contains(short, "showing") {
		t.Errorf("a 3-agent list should not claim a window:\n%s", short)
	}
	if !strings.Contains(short, "3 announced") {
		t.Errorf("a short list should state its count:\n%s", short)
	}
}

// Every line fits the pane at both spec widths, and no field is dropped when it
// gets tight — the cut is marked with … instead.
func TestQuietInspectorWidths(t *testing.T) {
	now := quietNow()
	rows := phantoms(12, now)
	for _, width := range []int{64, 44} {
		frame := quietFrame(rows, 0, width)
		for _, ln := range frame {
			if n := len([]rune(strings.TrimRight(ln, " "))); n > width {
				t.Errorf("width %d: line is %d columns: %q", width, n, ln)
			}
		}
		joined := strings.Join(frame, "\n")
		newest := rows[len(rows)-1]
		if !strings.Contains(joined, newest.SpawnedAt.Format(quietTimeFormat)) {
			t.Errorf("width %d lost the spawn time:\n%s", width, joined)
		}
		if !strings.Contains(joined, newest.LastEventAt.Format(quietTimeFormat)) {
			t.Errorf("width %d lost the last-event time:\n%s", width, joined)
		}
		if !strings.Contains(joined, quietLastHeader) {
			t.Errorf("width %d dropped the last-event column:\n%s", width, joined)
		}
	}

	// At 44 an 18-character platform id cannot fit; the cut must be marked AND
	// the rows must stay distinguishable, because phantom ids differ in their
	// tails (DATA-SOURCES: ids differing only in the last two characters).
	narrow := strings.Join(quietFrame(rows, 0, 44), "\n")
	if !strings.Contains(narrow, "…") {
		t.Errorf("narrow 44 truncated an id silently:\n%s", narrow)
	}
	seen := map[string]bool{}
	for _, ln := range quietFrame(rows, 0, 44) {
		if !strings.Contains(ln, "…") || strings.Contains(ln, "id ") {
			continue
		}
		cell := strings.Fields(ln)[0]
		if seen[cell] {
			t.Fatalf("narrow 44 renders two suppressed agents as %q — the ids are indistinguishable:\n%s", cell, narrow)
		}
		seen[cell] = true
	}
	if len(seen) < 2 {
		t.Fatalf("expected several truncated id cells to compare:\n%s", narrow)
	}
	if strings.Contains(strings.Join(quietFrame(rows, 0, 64), "\n"), rows[0].ID+"…") {
		t.Error("wide 64 truncated an id that fits")
	}
}

// state.QuietAgent has no times today. The adapter must say so rather than
// print a zero-value clock that reads as a real observation (C8).
func TestQuietInspectorHonestTimes(t *testing.T) {
	w := state.World{Quiet: []state.QuietAgent{
		{ID: "ada0620f224b32d63", Type: "general-purpose", Status: state.StatusDone},
	}}
	rows := quietRows(w)
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	if !rows[0].SpawnedAt.IsZero() || !rows[0].LastEventAt.IsZero() {
		t.Fatal("state.QuietAgent gained times — populate quietRows and drop this test")
	}
	frame := strings.Join(quietFrame(rows, 0, 64), "\n")
	if strings.Contains(frame, "00:00:00") {
		t.Errorf("a zero time was printed as an observation:\n%s", frame)
	}
	if !strings.Contains(frame, "—") {
		t.Errorf("the missing times are not marked absent:\n%s", frame)
	}
}

// ⏎ on the quiet line must land on THIS view, not on an agent inspector full of
// dashes. renderInspector routes on selQuiet; the keypress that sets it is the
// next phase's job, so this asserts the routing directly.
func TestInspectorRoutesQuietSelection(t *testing.T) {
	v := newUIState(testConfig())
	v.SelID = selQuiet
	v.InspectorOpen = true
	w := state.World{
		Session:   state.Session{ID: "s", ShortID: "s"},
		Connected: true,
		Quiet: []state.QuietAgent{
			{ID: "aaa1", Type: "general-purpose", Status: state.StatusDone},
			{ID: "bbb2", Type: "explore", Status: state.StatusRun},
		},
	}
	got := strings.Join(quietLinesOf(renderInspector(w, v, 64, quietNow(), NewPalette(2, true))), "\n")
	if !strings.Contains(got, "suppressed agents") {
		t.Fatalf("selQuiet did not open the suppressed-agents view:\n%s", got)
	}
	if strings.Contains(got, "UNKNOWN") {
		t.Errorf("selQuiet fell through to the agent inspector:\n%s", got)
	}
	if !strings.Contains(got, "aaa1") || !strings.Contains(got, "bbb2") {
		t.Errorf("not every suppressed agent is listed:\n%s", got)
	}
	if !strings.Contains(got, "▣") {
		t.Errorf("missing the §13.5 inspector glyph:\n%s", got)
	}
}

func quietLinesOf(lines [][]seg) []string {
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		out = append(out, stripANSI(renderSegs(ln)))
	}
	return out
}

// The degenerate case: opened with nothing to list, it says so.
func TestQuietInspectorEmpty(t *testing.T) {
	frame := strings.Join(quietFrame(nil, 0, 64), "\n")
	if !strings.Contains(frame, copyQuietNone) {
		t.Errorf("frame = %q", frame)
	}
}
