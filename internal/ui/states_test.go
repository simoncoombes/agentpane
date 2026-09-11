package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/state"
)

// §3.9 state table coverage beyond the snapshots.

func TestStateDisconnected(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	w.Connected = false
	v.DisconnectedAt = demoNow().Add(-1 * time.Second)
	frame, _ := renderFrame(w, v, 64, 44, demoNow(), NewPalette(2, false))
	plain := stripANSI(frame)
	if !strings.Contains(plain, copyDisconnected) {
		t.Errorf("header lost %q", copyDisconnected)
	}
	if !strings.Contains(plain, "retrying in ") {
		t.Errorf("footer lost the retry countdown")
	}
	// The tree region is dimmed via SGR 2 (§3.9): the main row line must
	// carry the faint attribute.
	for _, ln := range strings.Split(frame, "\n") {
		if strings.Contains(stripANSI(ln), "◍ main") {
			has2 := false
			for _, c := range sgrCodes(ln) {
				if c == "2" {
					has2 = true
				}
			}
			if !has2 {
				t.Errorf("disconnected tree not dimmed: %q", ln)
			}
		}
	}
}

func TestStateCompacting(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	w.Session.State = state.SessionCompacting
	frame, _ := renderFrame(w, v, 64, 44, demoNow(), NewPalette(2, false))
	if !strings.Contains(stripANSI(frame), "⠿ compacting context…") {
		t.Errorf("compacting footer missing")
	}
}

// Compaction holds row order: no reorder while ids may reset (§3.9).
func TestCompactionHoldsOrder(t *testing.T) {
	m, _ := newTestModel(t)
	at := m.clock
	m.ingest(spawnEvent("a1", "First agent", at))
	m.ingest(spawnEvent("a2", "Second agent", at))
	for _, id := range []string{"a1", "a2"} {
		m.ingest(event.Event{AgentID: id, Kind: event.ToolStart, Tool: "Read", Target: "f", At: at})
	}
	m.ingest(event.Event{AgentID: event.MainAgentID, Kind: event.CompactStarted, At: at})
	if len(m.v.HeldOrder) != 2 {
		t.Fatalf("order not held on compaction: %v", m.v.HeldOrder)
	}
	// a1 asks — normally it would jump to rank 0, but the held order wins.
	m.ingest(askFor("a2", at.Add(time.Second)))
	agents := m.v.orderedAgents(m.world)
	if agents[0].ID != "a1" || agents[1].ID != "a2" {
		t.Errorf("rows reordered during compaction: %s, %s", agents[0].ID, agents[1].ID)
	}
	m.ingest(event.Event{AgentID: event.MainAgentID, Kind: event.CompactFinished, At: at.Add(2 * time.Second)})
	if m.v.HeldOrder != nil {
		t.Errorf("held order not released after compaction")
	}
}

func TestStateSessionEnded(t *testing.T) {
	v := newUIState(testConfig())
	w := state.World{
		Session:   state.Session{ID: "3f9c", ShortID: "3f9c", State: state.SessionEnded},
		Connected: true,
		LastRun: &state.Run{
			ID: "3f9c-r1", Prompt: "port the auth routes",
			StartedAt: demoNow().Add(-5 * time.Minute), EndedAt: demoNow().Add(-1 * time.Minute),
			Tokens: 42000,
			Agents: []state.RunAgent{{ID: "a1", Name: "Port the auth routes", Status: state.StatusDone, Duration: 90 * time.Second, Tokens: 42000}},
		},
	}
	frame, _ := renderFrame(w, v, 64, 24, demoNow(), NewPalette(2, false))
	plain := stripANSI(frame)
	if !strings.Contains(plain, "session ended") {
		t.Errorf("session-ended footer missing:\n%s", plain)
	}
	if !strings.Contains(plain, "LAST RUN") || !strings.Contains(plain, "3f9c-r1") {
		t.Errorf("frozen final summary missing:\n%s", plain)
	}
}

func TestStateAllClear(t *testing.T) {
	now := demoNow()
	w := syntheticWorld(4, now)
	for i := range w.Agents {
		w.Agents[i].Status = state.StatusDone
		w.Agents[i].Station = 3
		w.Agents[i].DoneAt = now.Add(-5 * time.Second)
	}
	v := newUIState(testConfig())
	// Filtered (the default): every agent has landed, so the tree proper is
	// empty and all four are in the list at its foot.
	frame, _ := renderFrame(w, v, 64, 24, now, NewPalette(2, false))
	if got := stripANSI(frame); !strings.Contains(got, "RETURNED 4") {
		t.Errorf("the returned list is missing:\n%s", got)
	}
	// Shown: the settled rows are back and the spec's own all-clear line with
	// them.
	v.ShowFinished = true
	frame, _ = renderFrame(w, v, 64, 24, now, NewPalette(2, false))
	if got := stripANSI(frame); !strings.Contains(got, "all clear · 4 merged") {
		t.Errorf("all-clear line missing with the filter off:\n%s", got)
	}
}

func TestStateDetailUnavailable(t *testing.T) {
	now := demoNow()
	w := syntheticWorld(1, now)
	v := newUIState(testConfig())
	v.DetailUnavailable = "hooks not installed"
	frame, _ := renderFrame(w, v, 64, 20, now, NewPalette(2, false))
	if !strings.Contains(stripANSI(frame), "⚠ subagent detail unavailable — hooks not installed") {
		t.Errorf("detail-unavailable line missing")
	}
}

func TestStateCondensedUnder12Rows(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	frame, _ := renderFrame(w, v, 64, 10, demoNow(), NewPalette(2, false))
	lines := strings.Split(frame, "\n")
	if len(lines) != 10 {
		t.Fatalf("condensed frame = %d lines, want 10", len(lines))
	}
	plain := stripANSI(frame)
	if !strings.Contains(plain, "more agents ↓") {
		t.Errorf("condensed mode lost the overflow marker:\n%s", plain)
	}
	if !strings.Contains(plain, "⚑ fix-ts2345-fallout") {
		t.Errorf("condensed mode lost the alert row")
	}
}

func TestStateFirstRunIdleCopy(t *testing.T) {
	v := newUIState(testConfig())
	w := state.World{
		Session:   state.Session{State: state.SessionIdle},
		Connected: true,
	}
	frame, _ := renderFrame(w, v, 64, 16, demoNow(), NewPalette(2, false))
	if !strings.Contains(stripANSI(frame), copyFirstRun) {
		t.Errorf("first-run copy missing")
	}
}

func TestStateDroppedEventsInOverlay(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	w.DroppedEvents = 7
	v.Overlay = true
	v.SourceName = "fake"
	v.AccentReason = "accent 2 (green): Δ0.42"
	frame, _ := renderFrame(w, v, 64, 30, demoNow(), NewPalette(2, false))
	plain := stripANSI(frame)
	for _, want := range []string{"7 dropped events", "SOURCE fake", "ACCENT accent 2 (green)"} {
		if !strings.Contains(plain, want) {
			t.Errorf("overlay missing %q", want)
		}
	}
}

// Idle keys: 1–9 session switching (§5.1).
func TestIdleSessionSwitching(t *testing.T) {
	m, _ := newTestModel(t)
	switched := []string{}
	m.cfg.SwitchSession = func(row SessionRow) { switched = append(switched, row.ShortID) }
	m.v.Sessions = []SessionRow{
		{ShortID: "3f9c", Attached: true, Live: true},
		{ShortID: "2a71", Live: true, Idle: true},
	}

	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	if len(switched) != 1 || switched[0] != "2a71" {
		t.Errorf("session switch = %v", switched)
	}
	if m.v.Toast.Text != "attached 2a71" {
		t.Errorf("attach toast = %q", m.v.Toast.Text)
	}
	// Empty slot: no-op (§5.1).
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("9")})
	if len(switched) != 1 {
		t.Errorf("9 switched to an empty slot")
	}
	// Already-attached row: no-op — re-attaching would tear down and
	// rebuild the live source stack (and race the socket) for nothing.
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if len(switched) != 1 {
		t.Errorf("1 re-attached the already-attached session")
	}
}
