package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/state"
	"github.com/simoncoombes/agentpane/internal/term"
)

// footerOf renders the inspector for one agent with an open file and returns
// the footer: its plain text and the bytes the terminal actually receives.
func footerOf(t *testing.T, links bool) (plain, raw string) {
	t.Helper()
	now := demoNow()
	a := state.Agent{
		ID: "a1", Name: "Read the auth handler", Status: state.StatusRun,
		SpawnIndex: 1, SpawnedAt: now.Add(-time.Minute), LastEventAt: now,
		OpenFile: "/src/api/handlers.ts",
	}
	w := state.World{Agents: []state.Agent{a}, Connected: true}
	v := newUIState(testConfig())
	v.assignSlugsInSpawnOrder(w.Agents)
	v.SelID = a.ID
	v.InspectorOpen = true
	v.LinksOn = links
	e := term.Emitter{Enabled: links}
	v.Emitter = &e

	lines := renderInspector(w, v, 64, 24, now, NewPalette(2, false))
	last := lines[len(lines)-1]
	return segsPlain(last), renderSegs(last)
}

// The inspector footer's OSC 8 link survives the renderer.
//
// It did not always: the escape was baked into the seg's text, where flatten
// stripped the ESC on the way out and left `]8;;file:///src/…` on the row as
// literal text — in the default configuration, since links are on by default.
// The URL now rides on the style and is applied at print time.
func TestFooterHyperlinkSurvivesFlatten(t *testing.T) {
	plain, raw := footerOf(t, true)

	if strings.Contains(plain, "]8;;") {
		t.Errorf("the escape leaked into the row as text: %q", plain)
	}
	if !strings.Contains(plain, "o /src/api/handlers.ts") {
		t.Errorf("the footer lost the open file: %q", plain)
	}
	want := "\x1b]8;;file:///src/api/handlers.ts\x1b\\"
	if !strings.Contains(raw, want) {
		t.Errorf("no OSC 8 open in the rendered bytes: %q", raw)
	}
	if !strings.HasSuffix(raw, "\x1b]8;;\x1b\\") {
		t.Errorf("no OSC 8 close in the rendered bytes: %q", raw)
	}
	// The link has no display width, so the plain form is what the layout
	// budgeted: the footer must still fit the pane.
	if got := len([]rune(plain)); got > 64 {
		t.Errorf("footer is %d columns wide: %q", got, plain)
	}
}

// --no-links renders the same text with no escape at all.
func TestFooterHyperlinkOff(t *testing.T) {
	plain, raw := footerOf(t, false)
	if strings.Contains(raw, "\x1b]8") {
		t.Errorf("links are off but the footer emitted OSC 8: %q", raw)
	}
	if !strings.Contains(plain, "o /src/api/handlers.ts") {
		t.Errorf("the footer lost the open file: %q", plain)
	}
}
