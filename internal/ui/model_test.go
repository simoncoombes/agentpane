package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/simoncoombes/agentpane/internal/config"
	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/runstore"
	"github.com/simoncoombes/agentpane/internal/state"
	"github.com/simoncoombes/agentpane/internal/statefile"
	"github.com/simoncoombes/agentpane/internal/term"
)

type demoStub struct{ paused, stepped int }

func (d *demoStub) Pause()  { d.paused++ }
func (d *demoStub) Resume() {}
func (d *demoStub) Step()   { d.stepped++ }

// newTestModel wires a Model with a captured emitter and a demo stub so the
// content clock advances deterministically on tick().
func newTestModel(t *testing.T) (*Model, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	cfg := config.Default()
	m := New(Config{
		Machine: state.New(state.DefaultConfig()),
		Cfg:     cfg,
		Emitter: &term.Emitter{W: &buf, Enabled: true, CopyFallback: func(string) error { return nil }},
		Demo:    &demoStub{},
		Now:     func() time.Time { return demoNow() },
	})
	m.clock = demoNow()
	return m, &buf
}

func spawnEvent(id, name string, at time.Time) event.Event {
	return event.Event{AgentID: id, AgentName: name, AgentType: "general-purpose",
		Kind: event.AgentSpawned, At: at}
}

func askFor(id string, at time.Time) event.Event {
	return event.Event{AgentID: id, Kind: event.PermissionRequested, Tool: "Bash",
		Target: "rm -rf cache", At: at,
		Ask: &event.AskPayload{Kind: "command", Command: "rm -rf cache"}}
}

// countBells counts standalone BEL bytes; the BEL terminating an OSC sequence
// (title, badge, OSC 9) is not a bell.
func countBells(s string) int {
	n := 0
	inOSC := false
	for i := 0; i < len(s); i++ {
		switch {
		case inOSC:
			if s[i] == 0x07 {
				inOSC = false
			}
		case s[i] == 0x1b && i+1 < len(s) && s[i+1] == ']':
			inOSC = true
		case s[i] == 0x07:
			n++
		}
	}
	return n
}

// Escalation fires once per ask — band, bell, title, badge, attention — and
// never for a stall or a completion (§3.20, PART 10).
func TestEscalationOncePerAskOnly(t *testing.T) {
	m, buf := newTestModel(t)
	at := m.clock

	m.ingest(spawnEvent("a1", "Fix the fallout", at))
	m.ingest(event.Event{AgentID: "a1", Kind: event.ToolStart, Tool: "Bash", Target: "pnpm build", At: at})
	if got := strings.Count(buf.String(), "]1;⚑"); got != 0 {
		t.Fatalf("title set before any ask: %d", got)
	}

	m.ingest(askFor("a1", at.Add(time.Second)))
	out := buf.String()
	if got := strings.Count(out, "]1;⚑"); got != 1 {
		t.Errorf("title writes = %d, want 1", got)
	}
	if !strings.Contains(out, "1337;SetBadgeFormat=") {
		t.Errorf("badge not set on new ask")
	}
	if !strings.Contains(out, "1337;RequestAttention=1") {
		t.Errorf("no dock bounce on new ask")
	}
	if got := countBells(out); got != 1 {
		t.Errorf("bells = %d, want exactly 1 on a new ask", got)
	}

	// The same ask re-raised (dedupe) must not escalate again.
	m.ingest(askFor("a1", at.Add(2*time.Second)))
	m.ingest(askFor("ghost", at.Add(3*time.Second)))
	if got := strings.Count(buf.String(), "]1;⚑"); got != 1 {
		t.Errorf("re-raised ask escalated again: title writes = %d", got)
	}
	if got := countBells(buf.String()); got != 1 {
		t.Errorf("re-raised ask rang the bell again: bells = %d", got)
	}

	// Notification fires once after notify_after unanswered (§3.20 level 4).
	if strings.Contains(buf.String(), "\x1b]9;") {
		t.Fatalf("OSC 9 before notify_after")
	}
	for i := 0; i < 61; i++ {
		m.tick()
	}
	if got := strings.Count(buf.String(), "\x1b]9;"); got != 1 {
		t.Errorf("OSC 9 notifications = %d, want exactly 1", got)
	}
	for i := 0; i < 10; i++ {
		m.tick()
	}
	if got := strings.Count(buf.String(), "\x1b]9;"); got != 1 {
		t.Errorf("OSC 9 repeated: %d", got)
	}

	// Resolution clears title and badge.
	m.ingest(event.Event{AgentID: "a1", Kind: event.PermissionGranted, At: m.clock})
	m.ingest(event.Event{AgentID: "a1", Kind: event.ToolStart, Tool: "Bash", Target: "ls", At: m.clock.Add(time.Second)})
	m.ingest(event.Event{AgentID: "a1", Kind: event.ToolEnd, Tool: "Bash", Target: "ls", At: m.clock.Add(2 * time.Second)})
	if !strings.Contains(buf.String(), "\x1b]1;\a") {
		t.Errorf("title not cleared on resolution")
	}

	// A stall and a completion produce no escalation at all.
	mark := buf.Len()
	m.ingest(spawnEvent("a2", "Quiet agent", m.clock))
	m.ingest(event.Event{AgentID: "a2", Kind: event.ToolStart, Tool: "Bash", Target: "sleep", At: m.clock})
	for i := 0; i < 200; i++ {
		m.tick() // well past stuck_after
	}
	m.ingest(event.Event{AgentID: "a2", Kind: event.AgentReturned, At: m.clock})
	stuckOut := buf.String()[mark:]
	for _, esc := range []string{"]1;⚑", "]9;", "RequestAttention"} {
		if strings.Contains(stuckOut, esc) {
			t.Errorf("stall/completion escalated (%q found)", esc)
		}
	}
	if got := countBells(stuckOut); got != 0 {
		t.Errorf("stall/completion rang the bell %d times", got)
	}
}

// The away-digest records exactly the rows that changed while unfocused
// (§3.19, PART 10).
func TestDigestRecordsExactlyChangedRows(t *testing.T) {
	m, _ := newTestModel(t)
	at := m.clock
	m.ingest(spawnEvent("a1", "Port the auth routes", at))
	m.ingest(spawnEvent("a2", "Port the user routes", at))
	m.ingest(spawnEvent("a3", "Write the docs", at))
	for _, id := range []string{"a1", "a2", "a3"} {
		m.ingest(event.Event{AgentID: id, Kind: event.AgentStarted, At: at})
	}

	m.handleBlur()
	if !m.v.Away {
		t.Fatalf("blur did not start recording")
	}

	// a1 works, a2 returns; a3 stays silent.
	m.ingest(event.Event{AgentID: "a1", Kind: event.ToolStart, Tool: "Grep", Target: "x", At: at.Add(5 * time.Second)})
	m.ingest(event.Event{AgentID: "a2", Kind: event.AgentReturned, At: at.Add(6 * time.Second)})
	m.clock = at.Add(30 * time.Second)

	m.handleFocus()
	d := m.v.Digest
	if d == nil {
		t.Fatalf("no digest built on focus-in")
	}
	if !d.Marks["a1"] || !d.Marks["a2"] {
		t.Errorf("changed rows not marked: %v", d.Marks)
	}
	if d.Marks["a3"] {
		t.Errorf("unchanged a3 marked")
	}
	if len(d.Entries) != 2 {
		t.Fatalf("digest entries = %d, want 2", len(d.Entries))
	}
	// Ranked: returns (a2, rank 2) after any asks/stalls; here a1 is
	// "changed" (rank 3) so a2 leads.
	if d.Entries[0].Slug != "port-user-routes" || d.Entries[0].Kind != "returned" {
		t.Errorf("digest[0] = %+v, want the return first", d.Entries[0])
	}

	// esc dismisses the digest.
	m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.v.Digest != nil {
		t.Errorf("esc did not dismiss the digest")
	}
}

// Selection is preserved across reorders by id, never index (§3.13).
func TestSelectionSurvivesReorder(t *testing.T) {
	m, _ := newTestModel(t)
	at := m.clock
	m.ingest(spawnEvent("a1", "First agent", at))
	m.ingest(spawnEvent("a2", "Second agent", at))
	for _, id := range []string{"a1", "a2"} {
		m.ingest(event.Event{AgentID: id, Kind: event.ToolStart, Tool: "Read", Target: "f", At: at})
	}
	m.v.SelID = "a2"

	// a1 becomes an ask → jumps to rank 0, a2's index shifts.
	m.ingest(askFor("a1", at.Add(time.Second)))
	if m.v.SelID != "a2" {
		t.Fatalf("selection moved on reorder: %q", m.v.SelID)
	}
	frame, meta := renderFrame(m.world, m.v, 64, 30, m.clock, NewPalette(2, false))
	found := false
	for i, ln := range strings.Split(frame, "\n") {
		if strings.Contains(ln, "\x1b[7m") || strings.Contains(ln, ";7m") {
			if meta.lineIDs[i] == "a2" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("reverse-video selection not on a2 after reorder")
	}
}

// §5.1 key behaviours with exact §5.2 toast strings.
func TestKeymapToasts(t *testing.T) {
	m, _ := newTestModel(t)

	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if m.v.Toast.Text != copyReadOnly || !m.v.Toast.Refusal {
		t.Errorf("x toast = %+v", m.v.Toast)
	}

	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")})
	if m.v.WidthMode != "narrow" || m.v.Toast.Text != "layout: narrow 44" {
		t.Errorf("w toggle = %q toast %q", m.v.WidthMode, m.v.Toast.Text)
	}

	// The right column starts on the token burn rate, so the first t swaps
	// to time-since-last-event and the second swaps back.
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if m.v.RightCol != "since" || m.v.Toast.Text != "right column: since last event" {
		t.Errorf("t toggle = %q toast %q", m.v.RightCol, m.v.Toast.Text)
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if m.v.RightCol != "rate" || m.v.Toast.Text != "right column: token rate" {
		t.Errorf("t toggle back = %q toast %q", m.v.RightCol, m.v.Toast.Text)
	}

	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if m.v.Toast.Text != "nothing to yank" {
		t.Errorf("empty yank toast = %q", m.v.Toast.Text)
	}
}

// ctrl-c warns while agents are live, quits on the second press, and no code
// path anywhere signals a process (C5: the only side effects are terminal
// escapes and file writes).
func TestCtrlCWarnsThenQuits(t *testing.T) {
	m, _ := newTestModel(t)
	at := m.clock
	m.ingest(spawnEvent("a1", "Busy agent", at))
	m.ingest(event.Event{AgentID: "a1", Kind: event.ToolStart, Tool: "Bash", Target: "make", At: at})

	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if m.quit {
		t.Fatalf("quit on first ctrl-c with live agents")
	}
	if m.v.Toast.Text != "agents keep running · ctrl-c again to quit" {
		t.Errorf("warn toast = %q", m.v.Toast.Text)
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m.quit {
		t.Fatalf("second ctrl-c did not quit")
	}
}

// esc/q close the inspector only — q never quits the pane (§5.1).
func TestEscClosesInspectorOnly(t *testing.T) {
	m, _ := newTestModel(t)
	m.v.InspectorOpen = true
	m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.v.InspectorOpen {
		t.Errorf("esc did not close the inspector")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if m.quit {
		t.Errorf("q quit the pane")
	}
}

// The demo keys drive the injected controls (§1.4).
func TestDemoPauseStep(t *testing.T) {
	m, _ := newTestModel(t)
	stub := m.cfg.Demo.(*demoStub)
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	if stub.paused != 1 || !m.v.DemoPaused {
		t.Errorf("p did not pause: %+v", stub)
	}
	// Step is `.`, not `n`. It moved there when `n` took the voice cycle; the
	// voice is gone but the binding stayed (see TestDemoStepKeyIsDot).
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(".")})
	if stub.stepped != 1 {
		t.Errorf(". did not step: %+v", stub)
	}
	frame, _ := renderFrame(m.world, m.v, 64, 20, m.clock, NewPalette(2, false))
	if !strings.Contains(stripANSI(frame), "⏸ demo paused") {
		t.Errorf("paused footer missing")
	}
}

// A deduped ask collapses to ONE tree row carrying the retry count, and the
// folded retry agents never become rows of their own (§3.7.7, PART 10).
//
// This used to assert the band's `permission ×3` and `+2 retry agents folded`
// lines as well. That region is gone; the dedupe it was reporting on is not, and
// the tree is where it now has to be visible.
func TestDedupedAskOneTreeRowNoGhostRows(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	frame, _ := renderFrame(w, v, 64, 44, demoNow(), NewPalette(2, false))
	plain := stripANSI(frame)
	// The retry count rides on the row itself.
	if !strings.Contains(plain, "fix-ts2345-fallout  ↺2") {
		t.Errorf("the deduped row lost its ↺2 retry count:\n%s", plain)
	}
	if got := strings.Count(plain, "fix-ts2345-fallout"); got != 1 {
		t.Errorf("fix-ts2345-fallout appears %d times, want 1 (its tree row, no ghosts)", got)
	}
	if len(w.Agents) != 8 {
		t.Errorf("ghost agents leaked into the tree: %d rows", len(w.Agents))
	}
}

// Statefile updates carry the §3.20a shape and land on disk.
func TestStatefileSnapshotShape(t *testing.T) {
	sm, events := demoMachine(t)
	v, w := demoView(t, sm, events)
	model, _ := newTestModel(t)
	model.world = w
	model.v = v
	model.clock = demoNow()

	// No writer wired: must not panic (C9).
	model.writeStatefile()

	dir := t.TempDir()
	model.cfg.StateWriter = &statefile.Writer{Dir: dir, Now: func() time.Time { return demoNow() }}
	model.writeStatefile()

	s, ok := statefile.Read(dir, demoNow())
	if !ok {
		t.Fatalf("statefile missing after update")
	}
	if s.Asks != 1 || s.Stalls != 1 || s.Live != 3 || s.Settled != 1 {
		t.Errorf("statefile counts = %+v", s)
	}
	if s.OldestAskSlug != "fix-ts2345-fallout" {
		t.Errorf("oldest ask slug = %q", s.OldestAskSlug)
	}
	if s.OldestAskAge != 52*time.Second {
		t.Errorf("oldest ask age = %v", s.OldestAskAge)
	}
	if s.InCall != 1 {
		t.Errorf("in_call = %d, want 1 (run-auth-tests)", s.InCall)
	}
}

// A finished run is appended to the run store (§2.8, §5.3).
func TestRunEndedPersists(t *testing.T) {
	m, _ := newTestModel(t)
	m.cfg.RunStore = &runstore.Store{Dir: t.TempDir()}
	at := m.clock

	m.ingest(event.Event{AgentID: event.MainAgentID, Kind: event.SessionStart, At: at, SessionID: "cafe1234"})
	m.ingest(event.Event{AgentID: event.MainAgentID, Kind: event.UserPromptSubmitted, Detail: "do the thing", At: at})
	m.ingest(spawnEvent("a1", "Port the auth routes", at.Add(time.Second)))
	m.ingest(event.Event{AgentID: "a1", Kind: event.ToolStart, Tool: "Bash", Target: "make", At: at.Add(2 * time.Second)})
	m.ingest(event.Event{AgentID: "a1", Kind: event.ToolEnd, Tool: "Bash", Target: "make", At: at.Add(3 * time.Second)})
	m.ingest(event.Event{AgentID: "a1", Kind: event.AgentReturned, At: at.Add(4 * time.Second)})
	m.ingest(event.Event{AgentID: event.MainAgentID, Kind: event.SessionIdle, At: at.Add(5 * time.Second)})

	runs, _, err := m.cfg.RunStore.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("persisted runs = %d, want 1", len(runs))
	}
	if runs[0].Outcome != "all merged" {
		t.Errorf("outcome = %q", runs[0].Outcome)
	}
	if len(runs[0].Agents) != 1 || runs[0].Agents[0].Name != "port-auth-routes" {
		t.Errorf("run agents = %+v", runs[0].Agents)
	}
}

// Disconnect/reconnect drive the §3.9 state and the §5.2 toasts.
func TestDisconnectReconnectToasts(t *testing.T) {
	m, _ := newTestModel(t)
	m.ingest(event.Event{Kind: event.SourceDisconnected, At: m.clock})
	if m.world.Connected {
		t.Fatalf("still connected")
	}
	if m.v.Toast.Text != "⚠ disconnected — retrying" {
		t.Errorf("disconnect toast = %q", m.v.Toast.Text)
	}
	m.ingest(event.Event{Kind: event.SourceConnected, At: m.clock.Add(time.Second), Seq: 991})
	if m.v.Toast.Text != "✓ reconnected" {
		t.Errorf("reconnect toast = %q", m.v.Toast.Text)
	}
}

// ⏎ on the alert band jumps focus to the left pane instead of opening the
// inspector (§5.1).
func TestEnterOnBandJumpsFocus(t *testing.T) {
	m, _ := newTestModel(t)
	jumped := 0
	m.cfg.FocusLeftPane = func() error { jumped++; return nil }
	m.ingest(spawnEvent("a1", "Fix the fallout", m.clock))
	m.ingest(askFor("a1", m.clock))
	m.v.SelID = selBand
	m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if jumped != 1 {
		t.Errorf("focus jumps = %d, want 1", jumped)
	}
	if m.v.InspectorOpen {
		t.Errorf("inspector opened on the band")
	}
}
