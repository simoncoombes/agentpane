package ui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/simoncoombes/agentpane/internal/event"
)

func TestFormatSince(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{3 * time.Second, "3s"},
		{59 * time.Second, "59s"},
		{2*time.Minute + 50*time.Second, "2m50s"},
		{time.Hour + 4*time.Minute, "1h04m"},
		{-5 * time.Second, "0s"},
	}
	for _, c := range cases {
		if got := formatSince(c.d); got != c.want {
			t.Errorf("formatSince(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestFormatClockAndTokens(t *testing.T) {
	if got := formatClock(46 * time.Second); got != "0:46" {
		t.Errorf("formatClock = %q", got)
	}
	if got := formatClock(2*time.Minute + 32*time.Second); got != "2:32" {
		t.Errorf("formatClock = %q", got)
	}
	tok := []struct {
		n    int
		want string
	}{
		{0, ""}, {850, "850"}, {44000, "44k"}, {611000, "611k"}, {1500000, "1.5M"},
	}
	for _, c := range tok {
		if got := formatTokens(c.n); got != c.want {
			t.Errorf("formatTokens(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestPips(t *testing.T) {
	cases := []struct {
		station int
		done    bool
		want    string
	}{
		{0, false, "▫···"},
		{1, false, "▪▫··"},
		{2, false, "▪▪▫·"},
		{3, false, "▪▪▪▫"},
		{3, true, "▪▪▪▪"},
	}
	for _, c := range cases {
		if got := pips(c.station, c.done); got != c.want {
			t.Errorf("pips(%d,%v) = %q, want %q", c.station, c.done, got, c.want)
		}
	}
	if got := stationLabel(3, true); got != "returned" {
		t.Errorf("stationLabel done = %q", got)
	}
	if got := stationLabel(2, false); got != "first edit" {
		t.Errorf("stationLabel = %q", got)
	}
}

// Mouse: click selects by hit-testing the last frame; double-click inspects;
// wheel moves selection (§5.1).
func TestMouseSelectAndInspect(t *testing.T) {
	m, _ := newTestModel(t)
	at := m.clock
	m.ingest(spawnEvent("a1", "First agent", at))
	m.ingest(spawnEvent("a2", "Second agent", at))
	for _, id := range []string{"a1", "a2"} {
		m.ingest(event.Event{AgentID: id, Kind: event.ToolStart, Tool: "Read", Target: "f", At: at})
	}
	m.cols, m.rows = 64, 24
	m.frame, m.meta = renderFrame(m.world, m.v, m.cols, m.rows, m.clock, m.pal)

	target := -1
	for i, id := range m.meta.lineIDs {
		if id == "a2" {
			target = i
		}
	}
	if target == -1 {
		t.Fatalf("a2 not in frame meta")
	}
	click := tea.MouseMsg{X: 5, Y: target, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
	m.handleMouse(click)
	if m.v.SelID != "a2" {
		t.Fatalf("click did not select a2: %q", m.v.SelID)
	}
	m.handleMouse(click) // double click
	if !m.v.InspectorOpen {
		t.Errorf("double-click did not open the inspector")
	}

	m.v.InspectorOpen = false
	m.handleMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	if m.v.SelID != "a1" && m.v.SelID != selBand && m.v.SelID != event.MainAgentID {
		t.Errorf("wheel did not move selection: %q", m.v.SelID)
	}
}
