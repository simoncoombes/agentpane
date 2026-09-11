package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/simoncoombes/agentpane/internal/state"
)

// §13.1: tool text is flattened on INGEST for display, and `y` must yank the
// original bytes. A multi-line command pasted back as one line is not the command
// that ran, and being the command that ran is the entire point of the key.
//
// The assertion goes through the toast, which quotes the first 24 columns of
// whatever was copied: newlines have zero display width, so the flattened form
// cannot contain one and the raw form must.
func TestYankTakesTheRawCommandNotTheFlattenedOne(t *testing.T) {
	raw := "python3 - <<'EOF'\nimport json\nprint(json.dumps({'a': 1}))\nEOF"
	flat := "python3 - <<'EOF' import json print(json.dumps({'a': 1})) EOF"

	now := time.Now()
	agent := state.Agent{
		ID: "a1", Name: "run the json probe", NameExact: true, Status: state.StatusRun,
		SpawnedAt: now.Add(-time.Minute), LastEventAt: now,
		ActivityLine:    "Bash " + flat,
		RawActivityLine: "Bash " + raw,
		CallHistory: []state.Call{{
			Tool: "Bash", Target: flat, RawTarget: raw, Err: true, At: now,
		}},
		TotalCalls: 1,
	}
	w := state.World{
		Session:   state.Session{ID: "s", ShortID: "s", State: state.SessionActive},
		Main:      state.Main{Activity: "probing", LastEventAt: now},
		Agents:    []state.Agent{agent},
		Connected: true,
	}

	m := &Model{
		v: newUIState(testConfig()), pal: NewPalette(2, false),
		world: w, clock: now, cols: 64, rows: 44,
	}
	m.v.SelID = "a1"

	// The failing-command path.
	m.yankKey()
	if !strings.Contains(m.v.Toast.Text, "yanked") {
		t.Fatalf("nothing was yanked: %q", m.v.Toast.Text)
	}
	if !strings.Contains(m.v.Toast.Text, "\n") {
		t.Errorf("y yanked the flattened command: %q", m.v.Toast.Text)
	}

	// The activity-line fallback, with no failing call left to find.
	agents := append([]state.Agent(nil), w.Agents...)
	agents[0].CallHistory = nil
	w.Agents = agents
	m.world = w
	m.v.Toast = toast{}
	m.yankKey()
	if !strings.Contains(m.v.Toast.Text, "\n") {
		t.Errorf("y yanked the flattened activity line: %q", m.v.Toast.Text)
	}
}

// The demo step key is `.`. It moved off `n` when `n` took the voice (§13.2);
// the voice is gone but the binding stayed, because a keymap that shifts under
// readers who learned it is worse than an unused letter.
func TestDemoStepKeyIsDot(t *testing.T) {
	m, _ := newTestModel(t)
	stub := m.cfg.Demo.(*demoStub)
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if stub.stepped != 0 {
		t.Errorf("n stepped the demo %d times", stub.stepped)
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(".")})
	if stub.stepped != 1 {
		t.Errorf(". did not step the demo: %+v", stub)
	}
}
