package ui

// session_leak_test.go — THE standing rule: a pane pinned to session A must
// never display data originating from session B. The machine has dropped
// cross-session events from the WORLD for two rounds; these tests bind the
// other half, which is everything else the Model does per event — the
// inspector log above all, which is rendered, scrollable, and yankable.

import (
	"bytes"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/simoncoombes/agentpane/internal/config"
	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/state"
	"github.com/simoncoombes/agentpane/internal/statefile"
	"github.com/simoncoombes/agentpane/internal/term"
)

// siblingPermissionEvent is verbatim what internal/source/registry emits for
// ANY live session that hits a permission prompt (registry.waitingDetail).
func siblingPermissionEvent(session string, at time.Time) event.Event {
	return event.Event{
		SessionID: session,
		AgentID:   event.MainAgentID,
		Kind:      event.NotificationRaised,
		Detail:    "registry: waiting on permission",
		At:        at,
	}
}

// TestForeignEventNeverReachesTheInspectorLog is the regression: the pane is
// pinned to A, a sibling session B hits a permission prompt, and the row must
// leave no trace anywhere in this pane — not in the log, not on screen, not on
// the clipboard, not on the content clock.
func TestForeignEventNeverReachesTheInspectorLog(t *testing.T) {
	m, buf := newTestModel(t)
	m.cfg.Machine.Reset("session-A")
	m.world = m.cfg.Machine.Snapshot()
	clockBefore := m.clock

	// B's row is stamped a minute in the future: a foreign event must not drag
	// this pane's content clock either, which drives stalls and every "since".
	m.ingest(siblingPermissionEvent("session-B", clockBefore.Add(time.Minute)))

	if lines := m.v.Logs.Lines(event.MainAgentID); len(lines) != 0 {
		t.Fatalf("session B's row landed in the pinned pane's main log: %+v", lines)
	}
	if got := m.world.Asks; len(got) != 0 {
		t.Fatalf("world grew asks from session B: %+v", got)
	}
	if !m.clock.Equal(clockBefore) {
		t.Errorf("content clock advanced to %v on a foreign event (was %v)", m.clock, clockBefore)
	}
	if buf.Len() != 0 {
		t.Errorf("a foreign event produced terminal side effects: %q", buf.String())
	}

	// The inspector renders SelID, which defaults to main: nothing to show.
	m.v.InspectorOpen = true
	frame, _ := renderFrame(m.world, m.v, 64, 40, m.clock, m.pal)
	if strings.Contains(stripANSI(frame), "waiting on permission") {
		t.Errorf("session B's text is on screen in a pinned pane:\n%s", stripANSI(frame))
	}

	// Y yanks the whole log; an empty log has nothing to copy.
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Y")})
	if got := m.v.Toast.Text; got != "nothing to yank" {
		t.Errorf("Y toasted %q — session B's line was on the clipboard", got)
	}
}

// TestOwnSessionEventStillLogs is the control half: the same drop must not
// swallow the pane's OWN rows, or the fix is just a broken inspector.
func TestOwnSessionEventStillLogs(t *testing.T) {
	m, _ := newTestModel(t)
	m.cfg.Machine.Reset("session-A")
	m.world = m.cfg.Machine.Snapshot()

	m.ingest(siblingPermissionEvent("session-A", m.clock))
	lines := m.v.Logs.Lines(event.MainAgentID)
	if len(lines) != 1 || !strings.Contains(lines[0].Text, "waiting on permission") {
		t.Fatalf("the pane's own registry row is missing from its log: %+v", lines)
	}

	// Events with no session id are transport/hook lifecycle, genuinely
	// global, and keep passing.
	m.ingest(event.Event{AgentID: event.MainAgentID, Kind: event.NotificationRaised,
		Detail: "socket reconnected", At: m.clock})
	if got := len(m.v.Logs.Lines(event.MainAgentID)); got != 2 {
		t.Fatalf("session-less event dropped: log has %d lines", got)
	}
}

// TestForeignAgentWorkNeverReachesAnyLog covers the rest of the taxonomy, not
// just the registry's two kinds: a foreign FileEdited/ToolEnd/PermissionRequested
// must not create a log under its agent id either, which is what a stale
// post-switch hook event looks like.
func TestForeignAgentWorkNeverReachesAnyLog(t *testing.T) {
	m, _ := newTestModel(t)
	m.cfg.Machine.Reset("session-A")
	m.world = m.cfg.Machine.Snapshot()

	foreign := []event.Event{
		{SessionID: "session-B", AgentID: "b1", Kind: event.AgentSpawned, AgentName: "port routes", At: m.clock},
		{SessionID: "session-B", AgentID: "b1", Kind: event.FileEdited, Target: "/x/secret.go", LinesAdd: 4, At: m.clock},
		{SessionID: "session-B", AgentID: "b1", Kind: event.ToolEnd, Tool: "Bash", Target: "deploy prod", Err: "boom", At: m.clock},
		{SessionID: "session-B", AgentID: "b1", Kind: event.PermissionRequested, Tool: "Bash",
			Target: "rm -rf /", Ask: &event.AskPayload{Kind: "command", Command: "rm -rf /"}, At: m.clock},
	}
	for i, ev := range foreign {
		ev.Seq = uint64(i + 1)
		m.ingest(ev)
	}
	if lines := m.v.Logs.Lines("b1"); len(lines) != 0 {
		t.Fatalf("session B's agent grew a log in a pane pinned to A: %+v", lines)
	}
	if len(m.world.Agents) != 0 || len(m.world.Asks) != 0 {
		t.Fatalf("session B built rows in A's world: agents=%d asks=%d", len(m.world.Agents), len(m.world.Asks))
	}
}

// TestMachineAcceptsMatchesApply pins the two to each other. Accepts is only
// worth having as the single authority if it answers exactly what Apply does;
// if they drift, the log starts showing what the world does not.
func TestMachineAcceptsMatchesApply(t *testing.T) {
	cases := []struct {
		name    string
		pin     string
		evSess  string
		accepts bool
	}{
		{"pinned, own session", "session-A", "session-A", true},
		{"pinned, foreign session", "session-A", "session-B", false},
		{"pinned, session-less event", "session-A", "", true},
		{"unpinned, any session", "", "session-B", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mach := state.New(state.DefaultConfig())
			if c.pin != "" {
				mach.Reset(c.pin)
			}
			ev := event.Event{SessionID: c.evSess, AgentID: "a1", Kind: event.AgentSpawned,
				AgentName: "worker", Seq: 1, At: demoNow()}
			if got := mach.Accepts(ev); got != c.accepts {
				t.Fatalf("Accepts = %v, want %v", got, c.accepts)
			}
			mach.Apply(ev, demoNow())
			applied := len(mach.Snapshot().Agents) == 1
			if applied != c.accepts {
				t.Fatalf("Apply applied = %v but Accepts said %v — the log and the world disagree",
					applied, c.accepts)
			}
		})
	}
}

// TestUpdateLoopKeepsSessionsApart drives the whole Update path — the evMsg
// drain, the render, the escalation ladder and the state file — with A's and
// B's events interleaved, rather than calling ingest directly. A future
// per-event consumer added to that path is caught here even if it never
// touches the two call sites the other tests pin.
func TestUpdateLoopKeepsSessionsApart(t *testing.T) {
	dir := t.TempDir()
	writer := &statefile.Writer{Dir: dir, Session: "session-A", Interval: time.Nanosecond}
	ch := make(chan event.Event, 16)

	var buf bytes.Buffer
	mach := state.New(state.DefaultConfig())
	mach.Reset("session-A")
	m := New(Config{
		Machine:     mach,
		Cfg:         config.Default(),
		Events:      ch,
		Emitter:     &term.Emitter{W: &buf, Enabled: true, CopyFallback: func(string) error { return nil }},
		StateWriter: writer,
		Now:         func() time.Time { return demoNow() },
	})
	m.clock = demoNow()
	m.cols, m.rows = 64, 40

	at := demoNow()
	stream := []event.Event{
		{SessionID: "session-A", AgentID: event.MainAgentID, Kind: event.UserPromptSubmitted, Detail: "refactor", Seq: 1, At: at},
		{SessionID: "session-A", AgentID: "a1", Kind: event.AgentSpawned, AgentName: "explore schema", Seq: 2, At: at},
		// Everything below belongs to a sibling session sharing the directory.
		{SessionID: "session-B", AgentID: "b1", Kind: event.AgentSpawned, AgentName: "deploy to prod", Seq: 3, At: at},
		{SessionID: "session-B", AgentID: "b1", Kind: event.PermissionRequested, Tool: "Bash", Target: "rm -rf /",
			Ask: &event.AskPayload{Kind: "command", Command: "rm -rf /"}, Seq: 4, At: at},
		{SessionID: "session-B", AgentID: event.MainAgentID, Kind: event.NotificationRaised,
			Detail: "registry: waiting on permission", Seq: 5, At: at},
		{SessionID: "session-B", AgentID: event.MainAgentID, Kind: event.SessionIdle, Seq: 6, At: at},
	}
	for _, ev := range stream {
		m.Update(evMsg{ev, true})
	}
	m.Update(tickMsg(at))
	writer.Flush()

	// Render straight from the model's own state: View() returns the coalesced
	// cached frame (C10) and the injected clock never advances past minFrameGap.
	m.v.Anim = false // this is about session isolation, not the draw-in
	frame, _ := renderFrame(m.world, m.v, m.cols, m.rows, m.clock, m.pal)
	plain := stripANSI(frame)
	for _, forbidden := range []string{"deploy to prod", "rm -rf /", "waiting on permission"} {
		if strings.Contains(plain, forbidden) {
			t.Errorf("session B's %q is on a pinned pane's screen:\n%s", forbidden, plain)
		}
	}
	if !strings.Contains(plain, "explore") {
		t.Errorf("session A's own agent is missing:\n%s", plain)
	}
	for _, id := range []string{event.MainAgentID, "b1"} {
		for _, ln := range m.v.Logs.Lines(id) {
			if strings.Contains(ln.Text, "rm -rf /") || strings.Contains(ln.Text, "waiting on permission") {
				t.Errorf("session B leaked into the %s log: %q", id, ln.Text)
			}
		}
	}
	// §3.20a: B's ask must not raise this pane's status-bar count either, and
	// B going idle must not end A's run.
	s, ok := statefile.ReadSession(dir, "session-A", at)
	if !ok {
		t.Fatal("no state file written for session A")
	}
	if s.Asks != 0 {
		t.Errorf("state file counts %d asks — session B's prompt reached the status bar", s.Asks)
	}
	if m.world.Run == nil {
		t.Error("session B's idle ended session A's run")
	}
	if n := countBells(buf.String()); n != 0 {
		t.Errorf("session B's ask rang the bell %d time(s) in a pane pinned to A", n)
	}
}
